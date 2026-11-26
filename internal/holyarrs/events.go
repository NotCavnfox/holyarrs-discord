package holyarrs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// sub is one Events call's channel. Only the newest one is fed.
type sub struct {
	ch     chan core.Event
	stop   chan struct{}
	once   sync.Once
	mu     sync.Mutex // held while sending, so the channel never closes mid-send
	closed bool
}

func newSub() *sub {
	return &sub{ch: make(chan core.Event, 64), stop: make(chan struct{})}
}

// end closes the channel once; a send in progress gives up first.
func (s *sub) end() {
	s.once.Do(func() {
		close(s.stop)
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
}

func (s *sub) send(ev core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
	case <-s.stop:
	}
}

// Events returns a fresh channel of the events HolyArrs writes to stdin. It
// closes when ctx ends, when Events is called again (only the newest channel
// is fed) or when stdin ends. One goroutine reads stdin for the client's
// whole life, started by the first call.
func (c *Client) Events(ctx context.Context) (<-chan core.Event, error) {
	s := newSub()
	c.evMu.Lock()
	old := c.evCur
	c.evCur = s
	eof := c.evEOF
	c.evMu.Unlock()
	if old != nil {
		old.end()
	}
	if eof {
		s.end()
		return s.ch, nil
	}
	if c.stdin != nil {
		c.evOnce.Do(func() { go c.readEvents() })
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-s.stop:
		}
		s.end()
	}()
	return s.ch, nil
}

func (c *Client) readEvents() {
	br := bufio.NewReaderSize(c.stdin, 64<<10)
	for {
		line, tooLong, err := readLine(br, maxLine)
		switch {
		case tooLong:
			c.log.Warn("holyarrs: event line over the size limit, skipped", "limit", maxLine)
		case len(line) > 0:
			if ev, ok := c.parseEvent(line); ok {
				c.evMu.Lock()
				s := c.evCur
				c.evMu.Unlock()
				if s != nil {
					s.send(ev)
				}
			}
		}
		if err != nil {
			break // EOF or a broken pipe: HolyArrs is stopping the plugin
		}
	}
	c.evMu.Lock()
	c.evEOF = true
	s := c.evCur
	c.evMu.Unlock()
	if c.onClosed != nil {
		c.onClosed()
	}
	if s != nil {
		s.end()
	}
}

// readLine reads one line without its end. A line longer than limit is read
// to its end and reported as tooLong with no content.
func readLine(br *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, rerr := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > limit+2 {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimSpace(buf), tooLong, rerr
	}
}

var eventTypes = map[string]core.EventType{
	"library.add":       core.EventTitleAdded,
	"request.made":      core.EventRequestCreated,
	"request.approved":  core.EventRequestApproved,
	"request.declined":  core.EventRequestDeclined,
	"request.available": core.EventRequestAvailable,
}

// parseEvent turns one stdin line into an event; ok is false for a line to
// skip (not JSON, no event name, or a test message).
func (c *Client) parseEvent(line []byte) (core.Event, bool) {
	var env struct {
		Event   string          `json:"event"`
		At      flexTime        `json:"at"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &env); err != nil || env.Event == "" {
		c.log.Warn("holyarrs: unreadable event line, skipped", "bytes", len(line))
		return core.Event{}, false
	}
	type ref struct {
		ID flexInt `json:"id"`
	}
	var p struct {
		Test   bool                       `json:"test"`
		At     flexTime                   `json:"at"`
		Movie  *ref                       `json:"movie"`
		Show   *ref                       `json:"show"`
		Artist *ref                       `json:"artist"`
		Data   map[string]json.RawMessage `json:"data"`
	}
	if len(env.Payload) > 0 {
		// A payload of another shape (the "missed" notice) leaves p empty.
		_ = json.Unmarshal(env.Payload, &p)
	}
	if p.Test {
		return core.Event{}, false
	}
	ev := core.Event{Type: core.EventOther, WireType: env.Event, At: env.At.Time}
	if t, ok := eventTypes[env.Event]; ok {
		ev.Type = t
	}
	if ev.At.IsZero() {
		ev.At = p.At.Time
	}
	for _, r := range []*ref{p.Movie, p.Show, p.Artist} {
		if r != nil && r.ID > 0 {
			ev.TitleID = core.TitleID(r.ID)
			break
		}
	}
	if ev.TitleID == 0 {
		var id flexInt
		_ = id.UnmarshalJSON(p.Data["title_id"])
		ev.TitleID = core.TitleID(id)
	}
	var ask flexStr
	_ = ask.UnmarshalJSON(p.Data["ask_id"])
	ev.RequestID = string(ask)
	return ev, true
}
