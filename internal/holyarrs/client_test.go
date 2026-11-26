package holyarrs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const testToken = "hap_secret_token"

// sleeps records the waits a test client was asked for instead of sleeping.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func newTest(t *testing.T, h http.Handler) (*Client, *sleeps) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("%s %s: wrong Authorization header", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/", testToken, nil, nil, nil)
	sl := &sleeps{}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		sl.mu.Lock()
		sl.d = append(sl.d, d)
		sl.mu.Unlock()
		return ctx.Err()
	}
	c.jitter = func(d time.Duration) time.Duration { return d }
	return c, sl
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

const notLinkedMsg = "that person has not linked this plugin to their account"

func TestTransportSkipsProxy(t *testing.T) {
	c := New("http://127.0.0.1:1", "x", nil, nil, nil)
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("transport must be an *http.Transport with Proxy nil, got %T", c.hc.Transport)
	}
	if cap(c.sem) != 6 || c.budget != 480 || c.callTimeout != 30*time.Second || c.pollTimeout != 35*time.Second {
		t.Fatalf("defaults: sem %d budget %d call %v poll %v", cap(c.sem), c.budget, c.callTimeout, c.pollTimeout)
	}
	if got := fmt.Sprint(c.backoff); got != "[5s 15s 30s]" {
		t.Fatalf("backoff %s", got)
	}
}

func TestErrorMapping(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/titles/1", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "no such Title") })
	mux.HandleFunc("GET /api/v1/titles/2", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 403, "this plugin was not given the library.read permission")
	})
	mux.HandleFunc("GET /api/v1/titles/3", func(w http.ResponseWriter, r *http.Request) { fail(w, 403, notLinkedMsg) })
	mux.HandleFunc("GET /api/v1/titles/4", func(w http.ResponseWriter, r *http.Request) { fail(w, 500, "boom") })
	mux.HandleFunc("GET /api/v1/titles/5", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) })
	c, _ := newTest(t, mux)
	for id, want := range map[core.TitleID]error{1: core.ErrNotFound, 2: core.ErrForbidden, 3: core.ErrNotLinked} {
		_, err := c.Title(context.Background(), id)
		if !errors.Is(err, want) {
			t.Errorf("title %d: got %v, want %v", id, err, want)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("GET /api/v1/titles/%d", id)) || strings.Contains(err.Error(), testToken) {
			t.Errorf("title %d: error text %q", id, err)
		}
	}
	_, err := c.Title(context.Background(), 4)
	if err == nil || statusOf(err) != 500 || !strings.Contains(err.Error(), "boom") {
		t.Errorf("500: %v", err)
	}
	if _, err := c.Title(context.Background(), 5); err == nil || !strings.Contains(err.Error(), "GET /api/v1/titles/5") {
		t.Errorf("bad JSON: %v", err)
	}
}

func TestTransportErrorHidesNothingSecret(t *testing.T) {
	c := New("http://127.0.0.1:1", testToken, nil, nil, nil)
	_, err := c.Users(context.Background())
	if err == nil || strings.Contains(err.Error(), testToken) || !strings.Contains(err.Error(), "GET /api/v1/plugin-hooks/users") {
		t.Fatalf("got %v", err)
	}
}

func TestRetryOn429(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/plugin-hooks/users", func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			fail(w, 429, "this plugin is calling too often; try again in a minute")
		case 2:
			w.Header().Set("Retry-After", "7")
			fail(w, 429, "busy")
		default:
			writeJSON(w, 200, map[string]any{"users": []any{map[string]any{"id": 3, "name": "ann"}}})
		}
	})
	c, sl := newTest(t, mux)
	us, err := c.Users(context.Background())
	if err != nil || len(us) != 1 {
		t.Fatalf("users %v err %v", us, err)
	}
	if got := fmt.Sprint(sl.all()); got != "[5s 7s]" {
		t.Fatalf("waits %s", got)
	}
}

func TestRateLimitedAfterThreeRetries(t *testing.T) {
	var calls atomic.Int32
	c, sl := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fail(w, 429, "this plugin has too many calls open; try again when one ends")
	}))
	_, err := c.Users(context.Background())
	if !errors.Is(err, core.ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 4 || fmt.Sprint(sl.all()) != "[5s 15s 30s]" {
		t.Fatalf("calls %d waits %v", calls.Load(), sl.all())
	}
}

func TestRetryStopsWithContext(t *testing.T) {
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fail(w, 429, "busy") }))
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	if _, err := c.Users(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestSemaphoreCap(t *testing.T) {
	var cur, peak atomic.Int32
	release := make(chan struct{})
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		cur.Add(-1)
		writeJSON(w, 200, map[string]any{"users": []any{}})
	}))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Users(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for cur.Load() < maxInFlight && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(3 * time.Millisecond) // let any call over the cap show itself
	if got := cur.Load(); got != maxInFlight {
		t.Errorf("in flight %d, want %d", got, maxInFlight)
	}
	// A caller waiting for a slot leaves when its context ends.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Users(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting caller: %v", err)
	}
	close(release)
	wg.Wait()
	if peak.Load() > maxInFlight {
		t.Errorf("peak %d over the cap", peak.Load())
	}
}

func TestBudgetPerMinute(t *testing.T) {
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"users": []any{}})
	}))
	c.budget = 3
	var mu sync.Mutex
	clock := time.Unix(1_700_000_000, 0)
	var waits []time.Duration
	c.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		clock = clock.Add(d)
		mu.Unlock()
		return nil
	}
	for i := 0; i < 3; i++ {
		if _, err := c.Users(context.Background()); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		clock = clock.Add(10 * time.Second)
		mu.Unlock()
	}
	if len(waits) != 0 {
		t.Fatalf("waited inside the budget: %v", waits)
	}
	if _, err := c.Users(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(waits) != "[30s]" {
		t.Fatalf("waits %v, want one of 30s", waits)
	}
}

func TestBodyCap(t *testing.T) {
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"users":[],"pad":"`))
		_, _ = w.Write(make([]byte, maxBody))
	}))
	if _, err := c.Users(context.Background()); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("got %v", err)
	}
}

func TestUsersAndLinkCode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/plugin-hooks/users", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"users":[{"id":1,"name":"owner"},{"id":"7","name":"kim"},{"name":"ghost"}]}`))
	})
	mux.HandleFunc("POST /api/v1/plugin-hooks/link", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["external"] != "discord-42" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("link body %v", in)
		}
		_, _ = w.Write([]byte(`{"code":"1234-5678","expires":"2026-11-25T10:10:00Z"}`))
	})
	c, _ := newTest(t, mux)
	us, err := c.Users(context.Background())
	if err != nil || fmt.Sprint(us) != "[{1 owner} {7 kim}]" {
		t.Fatalf("users %v err %v", us, err)
	}
	code, exp, err := c.NewLinkCode(context.Background(), "discord-42")
	if err != nil || code != "1234-5678" || !exp.Equal(time.Date(2026, 11, 25, 10, 10, 0, 0, time.UTC)) {
		t.Fatalf("code %q exp %v err %v", code, exp, err)
	}
}

func TestLinks(t *testing.T) {
	body := `{"links":[
		{"user_id":7,"external":"302845117690216449","created_at":"2026-11-25T10:00:00Z"},
		{"user_id":"9","external":302845117690216450,"created_at":"2026-11-25T11:00:00.123456789Z"},
		{"external":"no user"},
		{"user_id":12,"external":"not-a-discord-id"}]}`
	status := 200
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/plugin-hooks/links", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get(forUserHeader) != "" {
			t.Errorf("links: %s, for-user %q", r.Method, r.Header.Get(forUserHeader))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	c, _ := newTest(t, mux)
	ctx := context.Background()

	got, err := c.Links(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("links %+v err %v", got, err)
	}
	want := []core.RemoteLink{
		{UserID: "7", External: "302845117690216449", CreatedAt: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)},
		{UserID: "9", External: "302845117690216450", CreatedAt: time.Date(2026, 11, 25, 11, 0, 0, 123456789, time.UTC)},
		{UserID: "12", External: "not-a-discord-id"},
	}
	for i := range want {
		if got[i].UserID != want[i].UserID || got[i].External != want[i].External || !got[i].CreatedAt.Equal(want[i].CreatedAt) {
			t.Errorf("link %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	body = `{"links":[]}`
	if got, err := c.Links(ctx); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty list: %v %v", got, err)
	}

	// An answer that is not the list must never read as "nobody is linked".
	for _, b := range []string{`{}`, `{"links":null}`, `<html>`, ``} {
		body = b
		if got, err := c.Links(ctx); err == nil || errors.Is(err, core.ErrUnsupported) {
			t.Errorf("body %q: got %v, err %v; want a plain error", b, got, err)
		}
	}

	// An older HolyArrs has no such route.
	body = `{"error":"no such route"}`
	for _, st := range []int{404, 405} {
		status = st
		_, err := c.Links(ctx)
		if !errors.Is(err, core.ErrUnsupported) || errors.Is(err, core.ErrNotFound) || statusOf(err) != st {
			t.Errorf("status %d: %v", st, err)
		}
		if !strings.Contains(err.Error(), "GET /api/v1/plugin-hooks/links") || strings.Contains(err.Error(), testToken) {
			t.Errorf("status %d: error text %q", st, err)
		}
	}
	for st, want := range map[int]error{403: core.ErrForbidden, 500: nil} {
		status = st
		_, err := c.Links(ctx)
		if err == nil || errors.Is(err, core.ErrUnsupported) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("status %d: %v", st, err)
		}
	}

	// HolyArrs 1.3.1 (seen live, 2026-11-26) answers a /plugin-hooks path it
	// does not know with this 403, not a 404.
	status, body = 403, `{"error":"plugins can't use this route"}`
	if _, err := c.Links(ctx); !errors.Is(err, core.ErrUnsupported) || statusOf(err) != 403 {
		t.Errorf("1.3.1 answer: %v", err)
	}
}

func TestLinksRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	c, sl := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fail(w, 429, "this plugin is calling too often")
			return
		}
		_, _ = w.Write([]byte(`{"links":[{"user_id":7,"external":"302845117690216449"}]}`))
	}))
	got, err := c.Links(context.Background())
	if err != nil || len(got) != 1 || calls.Load() != 2 || fmt.Sprint(sl.all()) != "[5s]" {
		t.Fatalf("links %v err %v calls %d waits %v", got, err, calls.Load(), sl.all())
	}
}

func TestRemoveLink(t *testing.T) {
	var deleted []string
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/plugin-hooks/links/{user}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(forUserHeader) != "" {
			t.Errorf("remove: for-user %q", r.Header.Get(forUserHeader))
		}
		switch u := r.PathValue("user"); u {
		case "7", "8": // 8 has no link: still 200
			deleted = append(deleted, u)
			_, _ = w.Write([]byte(`{"links":[{"user_id":9,"external":"302845117690216450","created_at":"2026-11-25T11:00:00Z"}]}`))
		case "20":
			fail(w, 403, "this plugin was not given the requests.users.read permission")
		case "21":
			fail(w, 500, "boom")
		case "22":
			_, _ = w.Write([]byte(`<html>`))
		default:
			fail(w, 400, "name the person by their user id")
		}
	})
	c, _ := newTest(t, mux)
	ctx := context.Background()
	for _, u := range []core.UserID{"7", "8"} {
		if err := c.RemoveLink(ctx, u); err != nil {
			t.Errorf("user %s: %v", u, err)
		}
	}
	if fmt.Sprint(deleted) != "[7 8]" {
		t.Errorf("deleted %v", deleted)
	}
	if err := c.RemoveLink(ctx, "20"); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("403: %v", err)
	}
	for _, u := range []core.UserID{"21", "22"} {
		if err := c.RemoveLink(ctx, u); err == nil || errors.Is(err, core.ErrUnsupported) {
			t.Errorf("user %s: %v, want a plain error", u, err)
		}
	}
	// A name is not an id: nothing is sent.
	for _, u := range []core.UserID{"bob", "", "-1", "7/../8"} {
		if err := c.RemoveLink(ctx, u); err == nil {
			t.Errorf("user %q: no error", u)
		}
	}
	if len(deleted) != 2 {
		t.Errorf("a bad id reached HolyArrs: %v", deleted)
	}

	// An older HolyArrs: the path is unknown, or known for GET only.
	for _, st := range []int{404, 405} {
		old, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fail(w, st, "nope") }))
		err := old.RemoveLink(ctx, "7")
		if !errors.Is(err, core.ErrUnsupported) || !strings.Contains(err.Error(), "DELETE /api/v1/plugin-hooks/links/7") {
			t.Errorf("status %d: %v", st, err)
		}
	}
}

func TestRecentlyAddedTitleAndPoster(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/home", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rows":["arrived"],"arrived":[
			{"key":"a9","kind":"show","title_id":9,"name":"Dark","year":2017,"line":"Season 2","href":"/library/9","poster":"/api/v1/artwork/p9.jpg","at":"2026-11-24T08:00:00.000000000Z"},
			{"key":"a3","kind":"movie","title_id":"3","name":"Heat","poster":"https://image.tmdb.org/t/p/w500/x.jpg","at":"2026-11-23T08:00:00.000000000Z"},
			{"key":"a0","kind":"movie","name":"no id"},
			{"key":"a4","kind":"artist","title_id":4,"name":"Low"}]}`))
	})
	mux.HandleFunc("GET /api/v1/titles/9", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":9,"kind":"show","name":"Dark","year":2017,"overview":"A town.","ids":{"tmdb":"70523"},
			"artwork":["/api/v1/artwork/p%209.jpg","https://cdn.example/x.jpg"],"added_at":"2026-11-01T12:00:00Z","size_bytes":5,"asks":[]}`))
	})
	mux.HandleFunc("GET /api/v1/titles/10", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":10,"kind":"movie","name":"Bare","artwork":[]}`))
	})
	mux.HandleFunc("GET /api/v1/artwork/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "p 9.jpg" || r.URL.Query().Get("size") != "500" {
			t.Errorf("artwork %q size %q", r.PathValue("name"), r.URL.Query().Get("size"))
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("JPEGDATA"))
	})
	c, _ := newTest(t, mux)
	ctx := context.Background()

	got, err := c.RecentlyAdded(ctx, 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("recently added %v err %v", got, err)
	}
	if got[0].ID != 9 || got[0].Kind != core.KindShow || got[0].PosterURL != "" || got[0].Year != 2017 ||
		!got[0].AddedAt.Equal(time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("first %+v", got[0])
	}
	if got[1].ID != 3 || got[1].PosterURL != "https://image.tmdb.org/t/p/w500/x.jpg" || got[2].Kind != core.KindAlbum || !got[2].AddedAt.IsZero() {
		t.Errorf("rest %+v %+v", got[1], got[2])
	}
	if got, _ := c.RecentlyAdded(ctx, 1); len(got) != 1 {
		t.Errorf("limit 1 gave %d", len(got))
	}

	ti, err := c.Title(ctx, 9)
	if err != nil || ti.Name != "Dark" || ti.Overview != "A town." || ti.PosterURL != "https://cdn.example/x.jpg" ||
		!ti.AddedAt.Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("title %+v err %v", ti, err)
	}
	data, ct, err := c.Poster(ctx, 9)
	if err != nil || string(data) != "JPEGDATA" || ct != "image/jpeg" {
		t.Errorf("poster %q %q %v", data, ct, err)
	}
	if _, _, err := c.Poster(ctx, 10); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("no artwork: %v", err)
	}
}

func TestWatchPeople(t *testing.T) {
	body := `{"enabled":true,"read_at":"2026-11-25T00:00:00Z","people":[
		{"server":"Jellyfin","server_kind":"jellyfin","name":"kim_jf","linked":true,"user_id":7,"user_name":"kim","movies":3,"shows":2,"episodes":40,"plays":51,"last_watched_at":"2026-11-24T20:00:00Z"},
		{"server":"Jellyfin","server_kind":"jellyfin","name":"guest","linked":false,"movies":1,"shows":0,"episodes":0,"plays":1}]}`
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/library/stats/watched" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	ps, err := c.WatchPeople(context.Background())
	if err != nil || len(ps) != 2 {
		t.Fatalf("%v %v", ps, err)
	}
	if ps[0].UserID != "7" || ps[0].UserName != "kim" || ps[0].Episodes != 40 || ps[0].Plays != 51 || ps[0].LastWatched.IsZero() {
		t.Errorf("first %+v", ps[0])
	}
	if ps[1].UserID != "" || ps[1].UserName != "guest" || !ps[1].LastWatched.IsZero() {
		t.Errorf("second %+v", ps[1])
	}
	body = `{"enabled":false,"people":[{"name":"x","plays":9}]}`
	if ps, err := c.WatchPeople(context.Background()); err != nil || ps == nil || len(ps) != 0 {
		t.Errorf("disabled: %v %v", ps, err)
	}
}

func TestRecentlyWatchedAndCleanup(t *testing.T) {
	enabled := true
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/library/watched-cleanup", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		people := []any{map[string]any{"key": "1:a", "name": "kim"}, map[string]any{"key": "1:b", "name": "lee"}}
		found := []any{}
		switch {
		case !q.Has("days"): // the default clean-up list
			found = []any{map[string]any{"id": 5, "kind": "movie", "name": "Heat", "year": 1995, "bytes": 9000, "last_watched_at": "2026-10-01T00:00:00Z"}}
		case q.Get("days") != "0":
			t.Errorf("days %q", q.Get("days"))
		case q.Get("people") == "1:a":
			found = []any{
				map[string]any{"id": 5, "kind": "movie", "name": "Heat", "year": 1995, "bytes": 9000, "last_watched_at": "2026-10-01T00:00:00Z"},
				map[string]any{"id": 6, "kind": "show", "name": "Dark", "bytes": 1, "last_watched_at": "2026-11-20T00:00:00Z"}}
		case q.Get("people") == "1:b":
			found = []any{map[string]any{"id": 5, "kind": "movie", "name": "Heat", "year": 1995, "bytes": 9000, "last_watched_at": "2026-11-22T00:00:00Z"}}
		}
		writeJSON(w, 200, map[string]any{"enabled": enabled, "days": 30, "people": people, "found": found})
	})
	mux.HandleFunc("GET /api/v1/asked-by", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"titles": map[string]any{"5": map[string]any{"askers": []any{map[string]any{"id": 7, "name": "kim"}}}}})
	})
	c, _ := newTest(t, mux)
	ctx := context.Background()

	ws, err := c.RecentlyWatched(ctx, 10)
	if err != nil || len(ws) != 2 {
		t.Fatalf("%v %v", ws, err)
	}
	if ws[0].Title.ID != 5 || !ws[0].LastWatched.Equal(time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)) || ws[1].Title.ID != 6 || ws[1].Title.Kind != core.KindShow {
		t.Errorf("order %+v", ws)
	}
	if ws, _ := c.RecentlyWatched(ctx, 1); len(ws) != 1 || ws[0].Title.ID != 5 {
		t.Errorf("limit %+v", ws)
	}

	cs, err := c.CleanupCandidates(ctx)
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %v", cs, err)
	}
	if cs[0].Title.Name != "Heat" || cs[0].Title.Year != 1995 || cs[0].SizeBytes != 9000 || cs[0].LastWatched.IsZero() || fmt.Sprint(cs[0].AskedBy) != "[7]" {
		t.Errorf("candidate %+v", cs[0])
	}

	enabled = false
	if ws, err := c.RecentlyWatched(ctx, 10); err != nil || len(ws) != 0 {
		t.Errorf("disabled watched: %v %v", ws, err)
	}
	if cs, err := c.CleanupCandidates(ctx); err != nil || len(cs) != 0 {
		t.Errorf("disabled cleanup: %v %v", cs, err)
	}
}

func TestUpcoming(t *testing.T) {
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v1/calendar" || q.Get("from") != "2026-11-25" || q.Get("to") != "2026-12-02" || q.Get("titles") != "all" {
			t.Errorf("calendar %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"dates":[
			{"title_id":2,"name":"Dune","year":2021,"kind":"digital","date":"2026-11-30T00:00:00Z","timed":false,"poster":"/api/v1/artwork/d.jpg"},
			{"title_id":9,"name":"Dark","kind":"episode","date":"2026-11-26T20:00:00Z","season":2,"episode":5,"episode_name":"The Bridge","timed":true},
			{"title_id":9,"name":"Dark","kind":"episode","date":"2026-11-27T20:00:00Z","season":2,"episode":6},
			{"title_id":4,"name":"Low","kind":"album","date":"2026-11-28T00:00:00Z","album":"Hey What"}]}`))
	}))
	c.now = func() time.Time { return time.Date(2026, 11, 25, 15, 4, 5, 0, time.UTC) }
	items, err := c.Upcoming(context.Background(), 7)
	if err != nil || len(items) != 4 {
		t.Fatalf("%v %v", items, err)
	}
	if items[0].Label != "S02E05 · The Bridge" || items[0].Title.Kind != core.KindShow || items[0].Title.ID != 9 {
		t.Errorf("episode %+v", items[0])
	}
	if items[1].Label != "S02E06" || items[2].Label != "Hey What" || items[2].Title.Kind != core.KindAlbum {
		t.Errorf("middle %+v %+v", items[1], items[2])
	}
	if items[3].Title.Name != "Dune" || items[3].Label != "" || items[3].Title.Kind != core.KindMovie || items[3].Title.PosterURL != "" || items[3].Title.Year != 2021 {
		t.Errorf("movie %+v", items[3])
	}
}

func TestAskedByBatches(t *testing.T) {
	var sizes []int
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["title_id"]
		sizes = append(sizes, len(ids))
		out := map[string]any{}
		for _, id := range ids {
			if id == "1" || id == "450" {
				out[id] = map[string]any{"askers": []any{map[string]any{"id": 7, "asked_at": "2026-01-01T00:00:00Z"}, map[string]any{"id": "8"}}}
			}
		}
		writeJSON(w, 200, map[string]any{"titles": out})
	}))
	ids := []core.TitleID{0, -3}
	for i := 1; i <= 450; i++ {
		ids = append(ids, core.TitleID(i))
	}
	got, err := c.AskedBy(context.Background(), ids)
	if err != nil || fmt.Sprint(sizes) != "[200 200 50]" {
		t.Fatalf("sizes %v err %v", sizes, err)
	}
	if len(got) != 2 || fmt.Sprint(got[1]) != "[7 8]" || fmt.Sprint(got[450]) != "[7 8]" {
		t.Errorf("got %v", got)
	}
	sizes = nil
	if got, err := c.AskedBy(context.Background(), nil); err != nil || len(got) != 0 || len(sizes) != 0 {
		t.Errorf("no ids: %v %v calls %v", got, err, sizes)
	}
}

func TestSearch(t *testing.T) {
	showsFail := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/titles/lookup", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("term") != "dark" || r.Header.Get(forUserHeader) != "" {
			t.Errorf("movie lookup %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"movies":[
			{"name":"Dark City","year":1998,"overview":"Night.","ids":{"tmdb":"2666","imdb":"tt0118929"},"poster":"https://image.tmdb.org/a.jpg","title_id":12,"never_add":false},
			{"name":"No id","year":2000,"ids":{}},
			{"name":"Darkman","year":1990,"ids":{"tmdb":9556},"never_add":false}],"attribution":"x"}`))
	})
	mux.HandleFunc("GET /api/v1/shows/lookup", func(w http.ResponseWriter, r *http.Request) {
		if showsFail {
			fail(w, 502, "metadata down")
			return
		}
		if r.URL.Query().Get("source") != "tmdb" {
			t.Errorf("show lookup %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"shows":[{"source":"tmdb","name":"Dark","year":2017,"status":"ended","ids":{"tmdb":"70523"},"poster":"/api/v1/artwork/x"}],"source":"tmdb"}`))
	})
	c, _ := newTest(t, mux)
	rs, err := c.Search(context.Background(), "  dark ")
	if err != nil || len(rs) != 3 {
		t.Fatalf("%v %v", rs, err)
	}
	if rs[0].Ref != "movie:tmdb:2666" || !rs[0].InLibrary || rs[0].PosterURL == "" || rs[0].Kind != core.KindMovie || rs[0].Overview != "Night." {
		t.Errorf("movie %+v", rs[0])
	}
	if rs[1].Ref != "show:tmdb:70523" || rs[1].InLibrary || rs[1].PosterURL != "" || rs[1].Kind != core.KindShow || rs[1].Year != 2017 {
		t.Errorf("show %+v", rs[1])
	}
	if rs[2].Ref != "movie:tmdb:9556" {
		t.Errorf("third %+v", rs[2])
	}
	if rs, err := c.Search(context.Background(), "   "); err != nil || len(rs) != 0 {
		t.Errorf("empty query: %v %v", rs, err)
	}
	showsFail = true
	if rs, err := c.Search(context.Background(), "dark"); err != nil || len(rs) != 2 {
		t.Errorf("one lookup down: %v %v", rs, err)
	}
}

func TestCreateRequest(t *testing.T) {
	mode := "ok"
	c, sl := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/asks" || r.Header.Get(forUserHeader) != "7" {
			t.Errorf("%s %s for %q", r.Method, r.URL.Path, r.Header.Get(forUserHeader))
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch mode {
		case "ok":
			if in["id"] != "tmdb:70523" || in["kind"] != "show" {
				t.Errorf("body %v", in)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"ask":{"id":31,"kind":"show","tmdb_id":"70523","name":"Dark","year":2017,"poster":"https://image.tmdb.org/d.jpg",
				"state":"asked","asked_by":{"id":7,"name":"kim"},"asked_at":"2026-11-25T09:00:00Z","people":[{"id":7,"name":"kim","asked_at":"2026-11-25T09:00:00Z"}]},"joined":false,"already":false}`))
		case "joined":
			_, _ = w.Write([]byte(`{"ask":{"id":"32","kind":"movie","name":"Heat","state":"available","title_id":5,"people":[{"id":2},{"id":7}]},"joined":true,"already":false}`))
		case "inlib":
			writeJSON(w, 409, map[string]any{"error": "Heat is already in the Library", "title_id": 5, "ready": true})
		case "conflict":
			fail(w, 409, "this was asked again")
		case "limit":
			w.Header().Set("Retry-After", "3600")
			writeJSON(w, 429, map[string]any{"error": "weekly limit reached", "retry_at": "2026-12-01T00:00:00Z", "per_week": 3, "kind": "movie"})
		case "unlinked":
			fail(w, 403, notLinkedMsg)
		case "kid":
			fail(w, 403, "above your age limit")
		}
	}))
	ctx := context.Background()
	show := core.SearchResult{Ref: "show:tmdb:70523", Name: "Dark", Kind: core.KindShow}
	req, err := c.CreateRequest(ctx, "7", show)
	if err != nil {
		t.Fatal(err)
	}
	if req.ID != "31" || req.Status != core.RequestPending || req.Title.ID != 0 || req.Title.Name != "Dark" || req.Title.Kind != core.KindShow ||
		req.Title.PosterURL == "" || fmt.Sprint(req.AskedBy) != "[7]" || req.CreatedAt.IsZero() {
		t.Errorf("request %+v", req)
	}
	mode = "joined"
	req, err = c.CreateRequest(ctx, "7", core.SearchResult{Ref: "movie:tmdb:949"})
	if err != nil || req.ID != "32" || req.Status != core.RequestAvailable || req.Title.ID != 5 || fmt.Sprint(req.AskedBy) != "[2 7]" {
		t.Errorf("joined %+v %v", req, err)
	}
	for m, want := range map[string]error{"inlib": core.ErrAlreadyAvailable, "limit": core.ErrLimit, "unlinked": core.ErrNotLinked, "kid": core.ErrForbidden} {
		mode = m
		if _, err := c.CreateRequest(ctx, "7", show); !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", m, err, want)
		}
	}
	if len(sl.all()) != 0 {
		t.Errorf("the weekly limit must not be retried, waits %v", sl.all())
	}
	mode = "conflict"
	if _, err := c.CreateRequest(ctx, "7", show); err == nil || errors.Is(err, core.ErrAlreadyAvailable) || !strings.Contains(err.Error(), "asked again") {
		t.Errorf("other conflict: %v", err)
	}
	if _, err := c.CreateRequest(ctx, "kim", show); !errors.Is(err, core.ErrNotLinked) {
		t.Errorf("name as id: %v", err)
	}
	for _, ref := range []string{"", "movie:imdb:tt1", "album:tmdb:1", "movie:tmdb:1x", "tmdb:5"} {
		if _, err := c.CreateRequest(ctx, "7", core.SearchResult{Ref: ref}); err == nil {
			t.Errorf("ref %q accepted", ref)
		}
	}
}

func TestDeleteTitle(t *testing.T) {
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Method != http.MethodPost || !in["apply"] || !in["delete_files"] {
			t.Errorf("%s %s %v", r.Method, r.URL.Path, in)
		}
		switch r.URL.Path {
		case "/api/v1/titles/5/delete":
			writeJSON(w, 200, map[string]any{"title_id": 5})
		case "/api/v1/titles/6/delete":
			fail(w, 403, "this plugin was not given the library.delete permission")
		default:
			fail(w, 404, "no such Title")
		}
	}))
	if err := c.DeleteTitle(context.Background(), 5); err != nil {
		t.Error(err)
	}
	if err := c.DeleteTitle(context.Background(), 6); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("got %v", err)
	}
	if err := c.DeleteTitle(context.Background(), 7); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

// pagesServer plays HolyArrs' side of the pages hook from a script of polls.
type pagesServer struct {
	mu      sync.Mutex
	puts    []string
	answers []map[string]any
	polls   []func(w http.ResponseWriter)
	done    chan struct{} // closed when the script ran out
	once    sync.Once
}

func (p *pagesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.URL.Path != pagesPath {
		fail(w, 404, "wrong path")
		return
	}
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		p.puts = append(p.puts, string(b))
		w.WriteHeader(204)
	case http.MethodGet:
		if len(p.polls) == 0 {
			p.once.Do(func() { close(p.done) })
			w.WriteHeader(204)
			return
		}
		next := p.polls[0]
		p.polls = p.polls[1:]
		next(w)
	case http.MethodPost:
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		p.answers = append(p.answers, in)
		if in["job"] == "late" {
			fail(w, 404, "no such job")
			return
		}
		w.WriteHeader(204)
	}
}

func job(id, op, path string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		writeJSON(w, 200, map[string]any{"id": id, "op": op, "payload": map[string]string{"path": path}})
	}
}

func TestServePages(t *testing.T) {
	ps := &pagesServer{done: make(chan struct{})}
	ps.polls = []func(http.ResponseWriter){
		func(w http.ResponseWriter) { w.WriteHeader(204) },
		job("j1", "file", "index.html"),
		job("j2", "file", "missing.js"),
		job("j3", "reticulate", ""),
		job("late", "file", "index.html"),
		func(w http.ResponseWriter) { fail(w, 500, "broken") },
		func(w http.ResponseWriter) { fail(w, 400, "the plugin is not running or does not provide this") },
		job("j4", "file", "index.html"),
	}
	c, sl := newTest(t, ps)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- c.ServePages(ctx, []core.Page{{ID: "link", Title: "Link", File: "index.html"}}, func(file string) ([]byte, string, bool) {
			if file == "index.html" {
				return []byte("<h1>hi</h1>"), "text/html", true
			}
			return nil, "", false
		})
	}()
	select {
	case <-ps.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pages script did not finish")
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("ServePages returned %v", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.puts) != 3 || ps.puts[0] != `[{"id":"link","title":"Link","file":"index.html"}]` {
		t.Errorf("declarations %q (want 3: the start and one after each error)", ps.puts)
	}
	if got := fmt.Sprint(sl.all()); got != "[1s 2s]" {
		t.Errorf("waits %s", got)
	}
	if len(ps.answers) != 5 {
		t.Fatalf("answers %v", ps.answers)
	}
	first := ps.answers[0]["data"].(map[string]any)
	if ps.answers[0]["job"] != "j1" || first["status"] != float64(200) || first["type"] != "text/html" ||
		first["body"] != base64.StdEncoding.EncodeToString([]byte("<h1>hi</h1>")) {
		t.Errorf("file answer %v", ps.answers[0])
	}
	if d := ps.answers[1]["data"].(map[string]any); d["status"] != float64(404) || d["body"] != nil {
		t.Errorf("missing file answer %v", ps.answers[1])
	}
	if ps.answers[2]["error"] == nil || ps.answers[2]["data"] != nil {
		t.Errorf("unknown job answer %v", ps.answers[2])
	}
	if ps.answers[4]["job"] != "j4" {
		t.Errorf("last answer %v", ps.answers[4])
	}
}

func TestServePagesStopsOn410(t *testing.T) {
	ps := &pagesServer{done: make(chan struct{})}
	ps.polls = []func(http.ResponseWriter){func(w http.ResponseWriter) { fail(w, 410, "the plugin was stopped") }}
	c, sl := newTest(t, ps)
	if err := c.ServePages(context.Background(), nil, nil); err != nil {
		t.Fatalf("got %v", err)
	}
	if len(ps.puts) != 1 || ps.puts[0] != "[]" || len(sl.all()) != 0 {
		t.Errorf("puts %q waits %v", ps.puts, sl.all())
	}
}

func TestServePagesBackoffCap(t *testing.T) {
	c, _ := newTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fail(w, 500, "down") }))
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 9 {
			cancel()
		}
		return ctx.Err()
	}
	if err := c.ServePages(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(waits); got != "[1s 2s 4s 8s 16s 32s 1m0s 1m0s 1m0s]" {
		t.Errorf("waits %s", got)
	}
}
