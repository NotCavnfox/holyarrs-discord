package announce

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

func onCfg() core.Config {
	return core.Config{EnableAnnounce: true, AnnounceChannel: "news"}
}

func addTitles(r *rig, n int) {
	for i := 1; i <= n; i++ {
		r.ha.titles[core.TitleID(i)] = core.Title{
			ID: core.TitleID(i), Name: fmt.Sprintf("Title *%d*", i), Year: 2000 + i,
			Kind: core.KindMovie, Overview: strings.Repeat("word ", 200), PosterURL: "https://img/p.jpg",
		}
	}
}

func added(id int) core.Event {
	return core.Event{Type: core.EventTitleAdded, TitleID: core.TitleID(id)}
}

func TestBatching(t *testing.T) {
	cases := []struct {
		name      string
		n         int
		wantPosts int
		summary   bool
	}{
		{"one", 1, 1, false},
		{"five stay separate", 5, 5, false},
		{"six become a summary", 6, 1, true},
		{"many", 40, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(onCfg())
			addTitles(r, c.n)
			f := New(r.deps)
			ctx := context.Background()
			for i := 1; i <= c.n; i++ {
				if !f.handle(ctx, added(i)) {
					t.Fatalf("title %d not queued", i)
				}
				if f.handle(ctx, added(i)) {
					t.Fatalf("title %d queued twice", i)
				}
			}
			f.flush(ctx)
			sends, _, _, _, _ := r.dc.snap()
			if len(sends) != c.wantPosts {
				t.Fatalf("posts = %d, want %d", len(sends), c.wantPosts)
			}
			e := sends[0].Msg.Embeds[0]
			if sends[0].Channel != "news" {
				t.Errorf("channel = %q", sends[0].Channel)
			}
			if c.summary {
				if !strings.HasPrefix(e.Title, fmt.Sprintf("%d new titles", c.n)) {
					t.Errorf("summary title = %q", e.Title)
				}
				if n := strings.Count(e.Description, "•"); n != c.n {
					t.Errorf("summary lists %d of %d", n, c.n)
				}
				if !strings.Contains(e.Description, `Title \*1\*`) {
					t.Errorf("not escaped: %q", e.Description)
				}
			} else {
				if e.Title != `Title \*1\* (2001)` || e.Thumbnail != "https://img/p.jpg" {
					t.Errorf("embed = %+v", e)
				}
				if n := utf8.RuneCountInString(e.Description); n > overviewLen || !strings.HasSuffix(e.Description, "…") {
					t.Errorf("overview not truncated: %d runes", n)
				}
				if len(e.Fields) != 2 || e.Fields[1].Value != "Movie" {
					t.Errorf("fields = %+v", e.Fields)
				}
			}
			// Nothing is announced twice, even by a new process.
			f2 := New(r.deps)
			for i := 1; i <= c.n; i++ {
				if f.handle(ctx, added(i)) || f2.handle(ctx, added(i)) {
					t.Fatalf("title %d announced again", i)
				}
			}
		})
	}
}

func TestPoster(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		data      []byte
		ctype     string
		err       error
		wantThumb string
		wantFile  string
	}{
		{"public url", "https://img/a.jpg", []byte("x"), "image/jpeg", nil, "https://img/a.jpg", ""},
		{"jpeg upload", "", []byte("x"), "image/jpeg", nil, "attachment://poster.jpg", "poster.jpg"},
		{"png upload", "", []byte("x"), "image/png", nil, "attachment://poster.png", "poster.png"},
		{"poster error ignored", "", nil, "", errors.New("boom"), "", ""},
		{"no poster", "", nil, "", core.ErrNotFound, "", ""},
		{"non-https url falls back", "http://lan/a.jpg", []byte("x"), "image/jpeg", nil, "attachment://poster.jpg", "poster.jpg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(onCfg())
			r.ha.titles[1] = core.Title{ID: 1, Name: "A", PosterURL: c.url}
			r.ha.poster, r.ha.posterType, r.ha.posterErr = c.data, c.ctype, c.err
			f := New(r.deps)
			f.handle(context.Background(), added(1))
			f.flush(context.Background())
			sends, _, _, _, _ := r.dc.snap()
			if len(sends) != 1 {
				t.Fatalf("posts = %d", len(sends))
			}
			m := sends[0].Msg
			if m.Embeds[0].Thumbnail != c.wantThumb {
				t.Errorf("thumbnail = %q want %q", m.Embeds[0].Thumbnail, c.wantThumb)
			}
			if c.wantFile == "" && len(m.Files) != 0 || c.wantFile != "" && (len(m.Files) != 1 || m.Files[0].Name != c.wantFile) {
				t.Errorf("files = %+v", m.Files)
			}
		})
	}
}

func TestNoChannelNoPosts(t *testing.T) {
	r := newRig(core.Config{EnableAnnounce: true})
	addTitles(r, 1)
	f := New(r.deps)
	if f.handle(context.Background(), added(1)) {
		t.Fatal("queued without a channel")
	}
}

func TestFailedPostIsNotMarked(t *testing.T) {
	r := newRig(onCfg())
	addTitles(r, 1)
	r.dc.sendErr = errors.New("down")
	f := New(r.deps)
	ctx := context.Background()
	f.handle(ctx, added(1))
	f.handle(ctx, added(99)) // unknown title: skipped
	f.flush(ctx)
	r.dc.mu.Lock()
	r.dc.sendErr = nil
	r.dc.mu.Unlock()
	if !f.handle(ctx, added(1)) {
		t.Fatal("a failed post should be retryable")
	}
}

func TestReadyDMs(t *testing.T) {
	cases := []struct {
		name    string
		ev      core.Event
		wantDMs []string
		text    string
		asked   int // AskedBy calls
	}{
		{"request available", core.Event{Type: core.EventRequestAvailable, TitleID: 7, RequestID: "r1"}, []string{"d1", "d2"}, `**Dune\_2** (2024) is now available.`, 1},
		{"title added", core.Event{Type: core.EventTitleAdded, TitleID: 7}, []string{"d1", "d2"}, `**Dune\_2** (2024) is now available.`, 1},
		{"title nobody asked for", core.Event{Type: core.EventTitleAdded, TitleID: 8}, nil, "", 1},
		{"title that is not there", core.Event{Type: core.EventRequestAvailable, TitleID: 9}, nil, "", 1},
		{"available without a title id", core.Event{Type: core.EventRequestAvailable, RequestID: "r1"}, nil, "", 0},
		{"approved is ignored", core.Event{Type: core.EventRequestApproved, TitleID: 7, RequestID: "r1"}, nil, "", 0},
		{"declined is ignored", core.Event{Type: core.EventRequestDeclined, TitleID: 7, RequestID: "r1"}, nil, "", 0},
		{"created is ignored", core.Event{Type: core.EventRequestCreated, TitleID: 7, RequestID: "r1"}, nil, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(core.Config{EnableAnnounce: true}) // no channel: DMs still go out
			r.ha.titles[7] = core.Title{ID: 7, Name: "Dune_2", Year: 2024}
			r.ha.titles[8] = core.Title{ID: 8, Name: "Eight"}
			// u1 asked twice, u3 has no link, u4 has DMs closed.
			r.ha.askedBy = map[core.TitleID][]core.UserID{7: {"u1", "u2", "u3", "u4", "u1"}, 9: {"u1"}}
			_ = r.links.Put(core.Link{DiscordID: "d1", UserID: "u1"})
			_ = r.links.Put(core.Link{DiscordID: "d2", UserID: "u2"})
			_ = r.links.Put(core.Link{DiscordID: "d4", UserID: "u4"})
			r.dc.dmErr["d4"] = fmt.Errorf("closed: %w", core.ErrForbidden)
			f := New(r.deps)
			ctx := context.Background()
			f.handle(ctx, c.ev)
			_, _, dms, _, _ := r.dc.snap()
			var got []string
			for _, d := range dms {
				got = append(got, d.Channel)
				if d.Msg.Content != c.text {
					t.Errorf("dm text = %q, want %q", d.Msg.Content, c.text)
				}
			}
			if strings.Join(got, ",") != strings.Join(c.wantDMs, ",") {
				t.Fatalf("dms to %v, want %v", got, c.wantDMs)
			}
			if n := r.ha.count("askedby"); n != c.asked {
				t.Fatalf("AskedBy calls = %d, want %d", n, c.asked)
			}
			if c.asked == 1 && (len(r.ha.asked[0]) != 1 || r.ha.asked[0][0] != c.ev.TitleID) {
				t.Errorf("AskedBy ids = %v", r.ha.asked)
			}
			// The same event again, the other event for the same title, and
			// both after a restart: nobody is told twice, and the closed-DM
			// member is not retried.
			for _, ft := range []*Feature{f, New(r.deps)} {
				ft.handle(ctx, c.ev)
				ft.handle(ctx, core.Event{Type: core.EventTitleAdded, TitleID: c.ev.TitleID})
				ft.handle(ctx, core.Event{Type: core.EventRequestAvailable, TitleID: c.ev.TitleID})
			}
			if _, _, again, _, _ := r.dc.snap(); len(again) != len(dms) && c.ev.Type != core.EventRequestApproved &&
				c.ev.Type != core.EventRequestDeclined && c.ev.Type != core.EventRequestCreated {
				t.Fatalf("duplicate DMs: %d then %d", len(dms), len(again))
			}
		})
	}
}

func TestReadyDMKeysAndRetries(t *testing.T) {
	r := newRig(onCfg())
	r.ha.titles[7] = core.Title{ID: 7, Name: "X"}
	r.ha.askedBy = map[core.TitleID][]core.UserID{7: {"u1", "u2"}}
	_ = r.links.Put(core.Link{DiscordID: "d1", UserID: "u1"})
	_ = r.links.Put(core.Link{DiscordID: "d2", UserID: "u2"})
	r.dc.dmErr["d2"] = errors.New("discord down")
	f := New(r.deps)
	ctx := context.Background()
	ev := core.Event{Type: core.EventRequestAvailable, TitleID: 7}

	r.ha.askedErr = errors.New("boom")
	f.handle(ctx, ev)
	if _, _, dms, _, _ := r.dc.snap(); len(dms) != 0 {
		t.Fatalf("dms = %d after a failed lookup", len(dms))
	}
	r.ha.mu.Lock()
	r.ha.askedErr = nil
	r.ha.mu.Unlock()

	f.handle(ctx, ev)
	var doc announcedDoc
	_ = r.store.Load(storeDoc, &doc)
	if strings.Join(doc.Keys, " ") != "ready:7:d1" {
		t.Fatalf("keys = %v", doc.Keys)
	}
	// A failed DM (not a closed one) is tried again on the next event.
	r.dc.mu.Lock()
	delete(r.dc.dmErr, "d2")
	r.dc.mu.Unlock()
	if !f.handle(ctx, core.Event{Type: core.EventTitleAdded, TitleID: 7}) {
		t.Fatal("title added must still be queued for the channel")
	}
	_, _, dms, _, _ := r.dc.snap()
	if len(dms) != 2 || dms[0].Channel != "d1" || dms[1].Channel != "d2" {
		t.Fatalf("dms = %+v", dms)
	}
}

func TestReadyDMWithoutTitleDetails(t *testing.T) {
	r := newRig(onCfg())
	r.ha.titles[7] = core.Title{ID: 7}
	r.ha.askedBy = map[core.TitleID][]core.UserID{7: {"u1"}}
	_ = r.links.Put(core.Link{DiscordID: "d1", UserID: "u1"})
	New(r.deps).handle(context.Background(), core.Event{Type: core.EventRequestAvailable, TitleID: 7})
	if _, _, dms, _, _ := r.dc.snap(); len(dms) != 1 || dms[0].Msg.Content != "A title you asked for is now available." {
		t.Fatalf("dms = %+v", dms)
	}
}

func TestDedupeListIsBounded(t *testing.T) {
	r := newRig(onCfg())
	f := New(r.deps)
	for i := 0; i < maxKeys+50; i++ {
		f.mark(fmt.Sprintf("added:%d", i))
	}
	if len(f.keys) != maxKeys || len(f.seen) != maxKeys {
		t.Fatalf("keys=%d seen=%d", len(f.keys), len(f.seen))
	}
	if f.seen["added:0"] || !f.seen[fmt.Sprintf("added:%d", maxKeys+49)] {
		t.Fatal("oldest keys should be dropped first")
	}
}

func TestRun(t *testing.T) {
	r := newRig(onCfg())
	addTitles(r, 8)
	f := New(r.deps)
	f.window = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	for i := 1; i <= 7; i++ {
		r.ev.ch <- added(i)
	}
	if !waitFor(func() bool { s, _, _, _, _ := r.dc.snap(); return len(s) == 1 }) {
		t.Fatal("no summary posted")
	}
	// A title still waiting at shutdown is posted on the way out.
	r.ev.ch <- added(8)
	if !waitFor(func() bool { return r.ha.count("title") >= 7 && len(r.ev.ch) == 0 }) {
		t.Fatal("event not consumed")
	}
	time.Sleep(2 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sends, _, _, _, _ := r.dc.snap()
	if len(sends) != 2 || !strings.HasPrefix(sends[0].Msg.Embeds[0].Title, "7 new titles") {
		t.Fatalf("sends = %d", len(sends))
	}
}

func TestRunDisabledWaits(t *testing.T) {
	r := newRig(core.Config{AnnounceChannel: "news"})
	addTitles(r, 1)
	f := New(r.deps)
	if f.Name() != "announce" || f.Commands() != nil {
		t.Fatal("name or commands")
	}
	r.ev.ch <- added(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	time.Sleep(3 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s, _, _, _, _ := r.dc.snap(); len(s) != 0 {
		t.Fatal("disabled feature posted")
	}
}

// gatedHA holds every AskedBy call until the gate opens, like a HolyArrs
// that is slow to answer.
type gatedHA struct {
	*fakeHA
	gate chan struct{}
}

func (h *gatedHA) AskedBy(ctx context.Context, ids []core.TitleID) (map[core.TitleID][]core.UserID, error) {
	select {
	case <-h.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return h.fakeHA.AskedBy(ctx, ids)
}

// A replay burst of 1000 events is taken off the bus at once, while the
// worker is stuck in a HolyArrs call, and every title is still announced.
func TestBurstIsAbsorbedWhileHolyArrsIsSlow(t *testing.T) {
	const n = 1000
	r := newRig(onCfg())
	addTitles(r, n)
	ha := &gatedHA{fakeHA: r.ha, gate: make(chan struct{})}
	r.deps.HA = ha
	ev := &countingEvents{ch: make(chan core.Event)} // unbuffered: a send needs the receiver
	r.deps.Events = ev
	f := New(r.deps)
	if got := ev.names(); len(got) != 1 || got[0] != "announce" {
		t.Fatalf("subscriptions after New = %v", got)
	}
	f.window = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	for i := 1; i <= n; i++ {
		select {
		case ev.ch <- added(i):
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d was not taken: the receive loop is blocked on HolyArrs", i)
		}
	}
	if !waitFor(func() bool { return f.queued() >= n-1 }) {
		t.Fatalf("queued = %d", f.queued())
	}
	if s, _, _, _, _ := r.dc.snap(); len(s) != 0 {
		t.Fatal("posted while HolyArrs was held")
	}
	close(ha.gate)
	deadline := time.Now().Add(20 * time.Second)
	total := 0
	for time.Now().Before(deadline) {
		total = 0
		sends, _, _, _, _ := r.dc.snap()
		for _, m := range sends {
			total += strings.Count(m.Msg.Embeds[0].Description, "•")
			if !strings.Contains(m.Msg.Embeds[0].Title, "new titles") {
				total++
			}
		}
		if f.queued() == 0 && r.ha.count("askedby") == n && r.ha.count("title") >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.ha.count("askedby"); got != n {
		t.Fatalf("handled %d of %d events", got, n)
	}
	f.load()
	missing := 0
	for i := 1; i <= n; i++ {
		if !f.seen[addedKey(core.TitleID(i))] && i > n-maxKeys {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("%d of the newest %d titles were never announced (lines seen: %d)", missing, maxKeys, total)
	}
	if got := ev.names(); len(got) != 1 {
		t.Fatalf("Run subscribed again: %v", got)
	}
}

func TestQueueIsCappedAndFiltered(t *testing.T) {
	r := newRig(onCfg())
	f := New(r.deps)
	f.enqueue(core.Event{Type: core.EventOther})
	f.enqueue(core.Event{Type: core.EventRequestCreated, TitleID: 1})
	if f.queued() != 0 {
		t.Fatalf("queued an event the worker ignores: %d", f.queued())
	}
	for i := 1; i <= maxQueue+500; i++ {
		f.enqueue(added(i))
	}
	if maxQueue != 2000 || f.queued() != maxQueue {
		t.Fatalf("queued = %d, cap = %d", f.queued(), maxQueue)
	}
	for i := 1; i <= maxQueue; i++ { // oldest first, the overflow is what was lost
		ev, ok := f.pop()
		if !ok || ev.TitleID != core.TitleID(i) {
			t.Fatalf("pop %d = %v %v", i, ev.TitleID, ok)
		}
	}
	if _, ok := f.pop(); ok || f.queued() != 0 {
		t.Fatal("queue not empty")
	}
	f.enqueue(core.Event{Type: core.EventRequestAvailable, TitleID: 9})
	if f.queued() != 1 {
		t.Fatal("no room after draining")
	}
	// A disabled announcer must not hold a channel nobody reads.
	ev := &countingEvents{ch: make(chan core.Event, 1)}
	off := newRig(core.Config{AnnounceChannel: "news"})
	off.deps.Events = ev
	New(off.deps)
	if got := ev.names(); len(got) != 0 {
		t.Fatalf("disabled announcer subscribed: %v", got)
	}
}

type countingEvents struct {
	mu   sync.Mutex
	subs []string
	ch   chan core.Event
}

func (e *countingEvents) Subscribe(name string) <-chan core.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subs = append(e.subs, name)
	return e.ch
}

func (e *countingEvents) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.subs...)
}
