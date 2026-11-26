// Package holyarrs implements core.HolyArrs over the HolyArrs plugin API: a
// Bearer client on the loopback target, JSON-line events on stdin and the
// pages hook long-poll. It translates wire shapes to core types and wire
// errors to core.Err*; nothing outside this package knows the wire format.
package holyarrs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	maxBody     = 8 << 20  // response body cap
	maxPoster   = 10 << 20 // poster body cap
	maxLine     = 1 << 20  // longest event line read from stdin
	maxInFlight = 6        // HolyArrs allows 8 open calls per plugin
	perMinute   = 480      // HolyArrs allows 600 calls per minute

	forUserHeader = "X-Holyarrs-For-User"
	notLinkedText = "has not linked"
)

// Client is the HolyArrs adapter. Make one with New.
type Client struct {
	base  string
	token string
	hc    *http.Client
	log   *slog.Logger
	sem   chan struct{}

	// Injectable for tests.
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	jitter      func(time.Duration) time.Duration
	backoff     []time.Duration // waits before each 429 retry
	callTimeout time.Duration
	pollTimeout time.Duration
	pagesMin    time.Duration
	pagesMax    time.Duration
	budget      int

	budgetMu sync.Mutex
	stamps   []time.Time // start times of the calls in the last minute

	stdin    io.Reader
	onClosed func()
	evOnce   sync.Once
	evMu     sync.Mutex
	evCur    *sub
	evEOF    bool
}

var _ core.HolyArrs = (*Client)(nil)

// New makes a client for target (http://127.0.0.1:<port>) with the plugin
// token. Events are read from stdin; onStdinClosed runs once when it ends,
// which means HolyArrs is stopping the plugin.
func New(target, token string, stdin io.Reader, onStdinClosed func(), log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		base:  strings.TrimRight(target, "/"),
		token: token,
		hc: &http.Client{
			// The target is loopback: never through the sandbox proxy.
			Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConnsPerHost: maxInFlight,
				IdleConnTimeout:     90 * time.Second,
			},
			// The token must not follow a redirect anywhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		log:         log,
		sem:         make(chan struct{}, maxInFlight),
		now:         time.Now,
		sleep:       sleepCtx,
		jitter:      func(d time.Duration) time.Duration { return d + rand.N(d/4+1) },
		backoff:     []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second},
		callTimeout: 30 * time.Second,
		pollTimeout: 35 * time.Second,
		pagesMin:    time.Second,
		pagesMax:    time.Minute,
		budget:      perMinute,
		stdin:       stdin,
		onClosed:    onStdinClosed,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// call is one request to HolyArrs.
type call struct {
	method  string
	path    string // without the query; this is what errors name
	query   url.Values
	body    any           // sent as JSON when not nil
	forUser core.UserID   // sets X-Holyarrs-For-User
	timeout time.Duration // 0: the default call timeout
	limit   int64         // 0: maxBody
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

// apiError is a refused or failed call. It names the method and path, never
// the token, and unwraps to the matching core error when there is one.
type apiError struct {
	method string
	path   string
	status int
	msg    string
	body   []byte
	kind   error
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("holyarrs: %s %s: status %d", e.method, e.path, e.status)
	if e.kind != nil {
		s += " (" + e.kind.Error() + ")"
	}
	if e.msg != "" {
		s += ": " + e.msg
	}
	return s
}

func (e *apiError) Unwrap() error { return e.kind }

// statusOf is the HTTP status of a refused call, 0 for any other error.
func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status
	}
	return 0
}

func (c *Client) fail(cl call, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // drop the URL: the path is named below
	}
	return fmt.Errorf("holyarrs: %s %s: %w", cl.method, cl.path, err)
}

// do sends cl, waiting for a free slot and the per-minute budget, and retries
// a 429. Any status outside 2xx comes back as an *apiError.
func (c *Client) do(ctx context.Context, cl call) (*reply, error) {
	var payload []byte
	if cl.body != nil {
		var err error
		if payload, err = json.Marshal(cl.body); err != nil {
			return nil, c.fail(cl, err)
		}
	}
	for attempt := 0; ; attempt++ {
		rep, err := c.once(ctx, cl, payload)
		if err != nil {
			return nil, c.fail(cl, err)
		}
		if rep.status != http.StatusTooManyRequests {
			return c.finish(cl, rep)
		}
		ae := &apiError{method: cl.method, path: cl.path, status: rep.status, msg: errorText(rep.body), body: rep.body, kind: core.ErrRateLimited}
		if hasField(rep.body, "per_week") {
			// The person's own weekly request limit: waiting does not help.
			ae.kind = core.ErrLimit
			return nil, ae
		}
		if attempt >= len(c.backoff) {
			return nil, ae
		}
		wait := retryAfter(rep.header)
		if wait <= 0 {
			wait = c.jitter(c.backoff[attempt])
		}
		c.log.Debug("holyarrs: rate limited, waiting", "method", cl.method, "path", cl.path, "wait", wait)
		if err := c.sleep(ctx, wait); err != nil {
			return nil, c.fail(cl, err)
		}
	}
}

func (c *Client) finish(cl call, rep *reply) (*reply, error) {
	if rep.status >= 200 && rep.status < 300 {
		return rep, nil
	}
	ae := &apiError{method: cl.method, path: cl.path, status: rep.status, msg: errorText(rep.body), body: rep.body}
	switch rep.status {
	case http.StatusForbidden:
		ae.kind = core.ErrForbidden
		if strings.Contains(ae.msg, notLinkedText) {
			ae.kind = core.ErrNotLinked
		}
	case http.StatusNotFound:
		ae.kind = core.ErrNotFound
	}
	return nil, ae
}

func (c *Client) once(ctx context.Context, cl call, payload []byte) (*reply, error) {
	if err := c.takeBudget(ctx); err != nil {
		return nil, err
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	timeout := cl.timeout
	if timeout <= 0 {
		timeout = c.callTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := c.base + cl.path
	if len(cl.query) > 0 {
		u += "?" + cl.query.Encode()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, u, body)
	if err != nil {
		return nil, errors.New("bad request address")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cl.forUser != "" {
		req.Header.Set(forUserHeader, string(cl.forUser))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limit := cl.limit
	if limit <= 0 {
		limit = maxBody
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response is larger than %d bytes", limit)
	}
	return &reply{status: resp.StatusCode, header: resp.Header, body: b}, nil
}

// takeBudget waits until fewer than budget calls started in the last minute.
// A sliding minute never lets a fixed minute on the server see more.
func (c *Client) takeBudget(ctx context.Context) error {
	for {
		c.budgetMu.Lock()
		now := c.now()
		cut := now.Add(-time.Minute)
		i := 0
		for i < len(c.stamps) && !c.stamps[i].After(cut) {
			i++
		}
		c.stamps = c.stamps[i:]
		if len(c.stamps) < c.budget {
			c.stamps = append(c.stamps, now)
			c.budgetMu.Unlock()
			return nil
		}
		wait := c.stamps[0].Add(time.Minute).Sub(now)
		c.budgetMu.Unlock()
		if wait <= 0 {
			wait = time.Millisecond
		}
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// getJSON does a GET and decodes the answer into v.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	cl := call{method: http.MethodGet, path: path, query: q}
	rep, err := c.do(ctx, cl)
	if err != nil {
		return err
	}
	return c.decode(cl, rep, v)
}

func (c *Client) decode(cl call, rep *reply, v any) error {
	if err := json.Unmarshal(rep.body, v); err != nil {
		return c.fail(cl, fmt.Errorf("unreadable answer: %w", err))
	}
	return nil
}

// retryAfter reads a Retry-After header in seconds, capped at two minutes.
func retryAfter(h http.Header) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || n <= 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, 2*time.Minute)
}

// errorText is the {"error": "..."} text of a body, or the start of the body.
func errorText(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	s := ""
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		s = e.Error
	} else {
		s = strings.TrimSpace(string(b))
	}
	s = strings.Map(func(r rune) rune {
		if r < ' ' {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.ToValidUTF8(s, "")
}

func hasField(b []byte, name string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m[name]
	return ok
}
