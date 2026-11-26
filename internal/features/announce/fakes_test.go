package announce

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

type createCall struct {
	As   core.UserID
	What core.SearchResult
}

type fakeHA struct {
	mu          sync.Mutex
	recent      []core.Title
	recentErr   error
	titles      map[core.TitleID]core.Title
	poster      []byte
	posterType  string
	posterErr   error
	people      []core.PersonWatch
	peopleErr   error
	watched     []core.WatchedTitle
	watchedErr  error
	upcoming    []core.CalendarItem
	upcomingErr error
	askedBy     map[core.TitleID][]core.UserID
	askedErr    error
	asked       [][]core.TitleID
	createResp  *core.Request // what CreateRequest returns; nil makes one up
	results     []core.SearchResult
	searchErr   error
	createErr   error
	created     []createCall
	calls       map[string]int
}

func (h *fakeHA) hit(name string) {
	if h.calls == nil {
		h.calls = map[string]int{}
	}
	h.calls[name]++
}

func (h *fakeHA) Users(context.Context) ([]core.User, error) { return nil, nil }
func (h *fakeHA) NewLinkCode(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, nil
}
func (h *fakeHA) Links(context.Context) ([]core.RemoteLink, error) { return nil, nil }
func (h *fakeHA) RemoveLink(context.Context, core.UserID) error    { return nil }
func (h *fakeHA) RecentlyAdded(_ context.Context, limit int) ([]core.Title, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("recent")
	if h.recentErr != nil {
		return nil, h.recentErr
	}
	out := append([]core.Title(nil), h.recent...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (h *fakeHA) Title(_ context.Context, id core.TitleID) (core.Title, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("title")
	t, ok := h.titles[id]
	if !ok {
		return core.Title{}, core.ErrNotFound
	}
	return t, nil
}
func (h *fakeHA) Poster(context.Context, core.TitleID) ([]byte, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("poster")
	return h.poster, h.posterType, h.posterErr
}
func (h *fakeHA) WatchPeople(context.Context) ([]core.PersonWatch, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("people")
	return append([]core.PersonWatch(nil), h.people...), h.peopleErr
}
func (h *fakeHA) RecentlyWatched(_ context.Context, limit int) ([]core.WatchedTitle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("watched")
	if h.watchedErr != nil {
		return nil, h.watchedErr
	}
	out := append([]core.WatchedTitle(nil), h.watched...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (h *fakeHA) Upcoming(context.Context, int) ([]core.CalendarItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("upcoming")
	return append([]core.CalendarItem(nil), h.upcoming...), h.upcomingErr
}
func (h *fakeHA) AskedBy(_ context.Context, ids []core.TitleID) (map[core.TitleID][]core.UserID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("askedby")
	h.asked = append(h.asked, append([]core.TitleID(nil), ids...))
	if h.askedErr != nil {
		return nil, h.askedErr
	}
	out := map[core.TitleID][]core.UserID{}
	for _, id := range ids {
		if who, ok := h.askedBy[id]; ok {
			out[id] = append([]core.UserID(nil), who...)
		}
	}
	return out, nil
}
func (h *fakeHA) Search(context.Context, string) ([]core.SearchResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("search")
	return append([]core.SearchResult(nil), h.results...), h.searchErr
}
func (h *fakeHA) CreateRequest(_ context.Context, as core.UserID, what core.SearchResult) (core.Request, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hit("create")
	if h.createErr != nil {
		return core.Request{}, h.createErr
	}
	h.created = append(h.created, createCall{as, what})
	if h.createResp != nil {
		return *h.createResp, nil
	}
	return core.Request{
		ID:      fmt.Sprintf("new-%d", len(h.created)),
		Title:   core.Title{Name: what.Name, Year: what.Year, Kind: what.Kind},
		Status:  core.RequestPending,
		AskedBy: []core.UserID{as},
	}, nil
}
func (h *fakeHA) CleanupCandidates(context.Context) ([]core.CleanupCandidate, error) {
	return nil, nil
}
func (h *fakeHA) DeleteTitle(context.Context, core.TitleID) error { return nil }
func (h *fakeHA) Events(context.Context) (<-chan core.Event, error) {
	return nil, nil
}
func (h *fakeHA) ServePages(context.Context, []core.Page, core.PageHandler) error { return nil }
func (h *fakeHA) SetHomeWidget(context.Context, core.Card) error                  { return nil }
func (h *fakeHA) count(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[name]
}

type sentMsg struct {
	Channel, ID string
	Msg         core.Message
}

type reply struct {
	Kind      string // respond, defer, followup, update
	Msg       core.Message
	Ephemeral bool
}

type fakeDC struct {
	mu      sync.Mutex
	next    int
	sends   []sentMsg
	edits   []sentMsg
	pins    []string
	dms     []sentMsg // Channel holds the user id
	replies []reply
	editErr map[string]error
	dmErr   map[string]error
	sendErr error
}

func (d *fakeDC) Open(context.Context, []core.Command, core.InteractionHandler) error { return nil }
func (d *fakeDC) Close() error                                                        { return nil }
func (d *fakeDC) Send(_ context.Context, ch string, m core.Message) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sendErr != nil {
		return "", d.sendErr
	}
	d.next++
	id := fmt.Sprintf("m%d", d.next)
	d.sends = append(d.sends, sentMsg{ch, id, m})
	return id, nil
}
func (d *fakeDC) Edit(_ context.Context, ch, id string, m core.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.editErr[id]; err != nil {
		return err
	}
	d.edits = append(d.edits, sentMsg{ch, id, m})
	return nil
}
func (d *fakeDC) Delete(context.Context, string, string) error { return nil }
func (d *fakeDC) Pin(_ context.Context, _, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pins = append(d.pins, id)
	return nil
}
func (d *fakeDC) DM(_ context.Context, user string, m core.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.dmErr[user]; err != nil {
		return err
	}
	d.dms = append(d.dms, sentMsg{Channel: user, Msg: m})
	return nil
}
func (d *fakeDC) AddRole(context.Context, string, string) error    { return nil }
func (d *fakeDC) RemoveRole(context.Context, string, string) error { return nil }
func (d *fakeDC) Respond(_ context.Context, _ *core.Interaction, m core.Message, eph bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replies = append(d.replies, reply{"respond", m, eph})
	return nil
}
func (d *fakeDC) Defer(_ context.Context, _ *core.Interaction, eph bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replies = append(d.replies, reply{"defer", core.Message{}, eph})
	return nil
}
func (d *fakeDC) FollowUp(_ context.Context, _ *core.Interaction, m core.Message, eph bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replies = append(d.replies, reply{"followup", m, eph})
	return nil
}
func (d *fakeDC) Update(_ context.Context, _ *core.Interaction, m core.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replies = append(d.replies, reply{"update", m, false})
	return nil
}
func (d *fakeDC) snap() (sends, edits, dms []sentMsg, pins []string, replies []reply) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]sentMsg(nil), d.sends...), append([]sentMsg(nil), d.edits...),
		append([]sentMsg(nil), d.dms...), append([]string(nil), d.pins...), append([]reply(nil), d.replies...)
}
func (d *fakeDC) lastReply() reply {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.replies) == 0 {
		return reply{}
	}
	return d.replies[len(d.replies)-1]
}

type fakeStore struct {
	mu   sync.Mutex
	docs map[string][]byte
}

func (s *fakeStore) Load(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.docs[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(b, v)
}
func (s *fakeStore) Save(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs == nil {
		s.docs = map[string][]byte{}
	}
	s.docs[name] = b
	return nil
}

type fakeLinks struct {
	mu    sync.Mutex
	links map[string]core.Link
}

func (l *fakeLinks) ByDiscord(id string) (core.Link, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.links[id]
	return v, ok
}
func (l *fakeLinks) ByUser(u core.UserID) (core.Link, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range l.links {
		if v.UserID == u {
			return v, true
		}
	}
	return core.Link{}, false
}
func (l *fakeLinks) All() []core.Link { return nil }
func (l *fakeLinks) Put(v core.Link) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.links == nil {
		l.links = map[string]core.Link{}
	}
	l.links[v.DiscordID] = v
	return nil
}
func (l *fakeLinks) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.links, id)
	return nil
}

type fakeEvents struct{ ch chan core.Event }

func (e *fakeEvents) Subscribe(string) <-chan core.Event { return e.ch }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	ha    *fakeHA
	dc    *fakeDC
	store *fakeStore
	links *fakeLinks
	ev    *fakeEvents
	clock *clock
	deps  core.Deps
}

func newRig(cfg core.Config) *rig {
	r := &rig{
		ha:    &fakeHA{titles: map[core.TitleID]core.Title{}},
		dc:    &fakeDC{editErr: map[string]error{}, dmErr: map[string]error{}},
		store: &fakeStore{},
		links: &fakeLinks{},
		ev:    &fakeEvents{ch: make(chan core.Event, 64)},
		clock: &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)},
	}
	r.deps = core.Deps{
		Cfg: cfg, HA: r.ha, DC: r.dc, Store: r.store, Links: r.links, Events: r.ev,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: r.clock.Now,
	}
	return r
}

// waitFor polls cond for up to two seconds without long sleeps.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}
