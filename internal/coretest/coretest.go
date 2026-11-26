// Package coretest holds in-memory fakes of the core ports for tests of other
// packages. Every fake is safe for concurrent use and touches no network or
// disk.
package coretest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

var (
	_ core.Store    = (*FakeStore)(nil)
	_ core.Links    = (*FakeLinks)(nil)
	_ core.Events   = (*FakeEvents)(nil)
	_ core.Discord  = (*FakeDiscord)(nil)
	_ core.HolyArrs = (*FakeHolyArrs)(nil)
)

// Call is one recorded method call with its arguments (ctx left out).
type Call struct {
	Method string
	Args   []any
}

// Recorder records calls in order and holds one settable error per method.
// It is embedded in every fake.
type Recorder struct {
	mu    sync.Mutex
	calls []Call
	errs  map[string]error
}

// SetErr makes method (for example "Send") return err until cleared with nil.
func (r *Recorder) SetErr(method string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errs == nil {
		r.errs = make(map[string]error)
	}
	if err == nil {
		delete(r.errs, method)
		return
	}
	r.errs[method] = err
}

// Calls returns a copy of every call so far, in order.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// CallsTo returns the calls to one method, in order.
func (r *Recorder) CallsTo(method string) []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Call
	for _, c := range r.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// ResetCalls forgets the recorded calls (errors stay set).
func (r *Recorder) ResetCalls() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

// record must be called with r.mu held.
func (r *Recorder) record(method string, args ...any) error {
	r.calls = append(r.calls, Call{Method: method, Args: args})
	return r.errs[method]
}

// Eventually polls cond every millisecond for up to two seconds and fails
// the test when it never turns true.
func Eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ---- Store ----

// FakeStore is a map-backed core.Store. Values go through JSON both ways, so
// a type that does not round-trip fails here as it would on disk.
// Methods recorded: "Load", "Save".
type FakeStore struct {
	Recorder
	docs map[string][]byte
}

func NewFakeStore() *FakeStore { return &FakeStore{docs: make(map[string][]byte)} }

func (s *FakeStore) Load(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("Load", name); err != nil {
		return err
	}
	data, ok := s.docs[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(data, v)
}

func (s *FakeStore) Save(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("Save", name); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.docs[name] = data
	return nil
}

// Raw returns the stored JSON for name.
func (s *FakeStore) Raw(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.docs[name]
	return append([]byte(nil), data...), ok
}

// SetRaw stores raw bytes for name, valid JSON or not.
func (s *FakeStore) SetRaw(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[name] = append([]byte(nil), data...)
}

// ---- Links ----

// ErrUserLinked is what FakeLinks.Put returns when the HolyArrs user already
// belongs to another Discord id. It wraps core.ErrAlreadyLinked.
var ErrUserLinked = fmt.Errorf("coretest: %w", core.ErrAlreadyLinked)

// FakeLinks is an in-memory core.Links with the same rules as the real book.
// Methods recorded (and failable): "Put", "Remove".
type FakeLinks struct {
	Recorder
	byDiscord map[string]core.Link
}

func NewFakeLinks(initial ...core.Link) *FakeLinks {
	f := &FakeLinks{byDiscord: make(map[string]core.Link)}
	for _, l := range initial {
		f.byDiscord[l.DiscordID] = l
	}
	return f
}

func (f *FakeLinks) ByDiscord(discordID string) (core.Link, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.byDiscord[discordID]
	return l, ok
}

func (f *FakeLinks) ByUser(user core.UserID) (core.Link, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.byDiscord {
		if l.UserID == user {
			return l, true
		}
	}
	return core.Link{}, false
}

func (f *FakeLinks) All() []core.Link {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.Link, 0, len(f.byDiscord))
	for _, l := range f.byDiscord {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DiscordID < out[j].DiscordID })
	return out
}

func (f *FakeLinks) Put(l core.Link) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Put", l); err != nil {
		return err
	}
	if l.DiscordID == "" || l.UserID == "" {
		return errors.New("coretest: link needs a Discord id and a user id")
	}
	for _, other := range f.byDiscord {
		if other.UserID == l.UserID && other.DiscordID != l.DiscordID {
			return ErrUserLinked
		}
	}
	f.byDiscord[l.DiscordID] = l
	return nil
}

func (f *FakeLinks) Remove(discordID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Remove", discordID); err != nil {
		return err
	}
	delete(f.byDiscord, discordID)
	return nil
}

// ---- Events ----

// FakeEvents is a core.Events the test feeds with Emit.
type FakeEvents struct {
	mu     sync.Mutex
	subs   map[string]chan core.Event
	closed bool
}

func NewFakeEvents() *FakeEvents { return &FakeEvents{subs: make(map[string]chan core.Event)} }

// Subscribe returns the channel for name (buffer 64); the same name gets the
// same channel.
func (f *FakeEvents) Subscribe(name string) <-chan core.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.subs[name]; ok {
		return ch
	}
	ch := make(chan core.Event, 64)
	if f.closed {
		close(ch)
	}
	f.subs[name] = ch
	return ch
}

// Emit hands e to every subscriber without blocking and returns how many
// took it (a full subscriber drops it).
func (f *FakeEvents) Emit(e core.Event) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0
	}
	n := 0
	for _, ch := range f.subs {
		select {
		case ch <- e:
			n++
		default:
		}
	}
	return n
}

// Subscribers returns the subscribed names, sorted.
func (f *FakeEvents) Subscribers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.subs))
	for name := range f.subs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Close closes every subscriber channel. Safe to call twice.
func (f *FakeEvents) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	for _, ch := range f.subs {
		close(ch)
	}
}

// ---- Discord ----

// FakeDiscord records every call in order. Method names are the interface
// method names ("Open", "Close", "Send", "Edit", "Delete", "Pin", "DM",
// "AddRole", "RemoveRole", "Respond", "Defer", "FollowUp", "Update"); Args
// are the arguments after ctx. Send returns "1", "2", ...
type FakeDiscord struct {
	Recorder
	nextID int
	cmds   []core.Command
	router core.InteractionHandler
	open   bool
}

func NewFakeDiscord() *FakeDiscord { return &FakeDiscord{} }

func (d *FakeDiscord) call(method string, args ...any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.record(method, args...)
}

func (d *FakeDiscord) Open(ctx context.Context, cmds []core.Command, router core.InteractionHandler) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.record("Open", cmds, router); err != nil {
		return err
	}
	d.cmds, d.router, d.open = append([]core.Command(nil), cmds...), router, true
	return nil
}

func (d *FakeDiscord) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open = false
	return d.record("Close")
}

// IsOpen reports whether Open succeeded and Close has not been called since.
func (d *FakeDiscord) IsOpen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.open
}

// Commands returns the commands passed to the last successful Open.
func (d *FakeDiscord) Commands() []core.Command {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]core.Command(nil), d.cmds...)
}

// Press delivers a component interaction to the router given to Open.
func (d *FakeDiscord) Press(ctx context.Context, in *core.Interaction) error {
	d.mu.Lock()
	router := d.router
	d.mu.Unlock()
	if router == nil {
		return errors.New("coretest: Discord was not opened with a router")
	}
	return router(ctx, in)
}

// Invoke runs the handler of the registered slash command named by
// in.Command ("name" or "parent sub").
func (d *FakeDiscord) Invoke(ctx context.Context, in *core.Interaction) error {
	for _, c := range d.Commands() {
		if c.Name == in.Command && c.Handle != nil {
			return c.Handle(ctx, in)
		}
		for _, s := range c.Sub {
			if c.Name+" "+s.Name == in.Command && s.Handle != nil {
				return s.Handle(ctx, in)
			}
		}
	}
	return errors.New("coretest: no command " + strconv.Quote(in.Command))
}

func (d *FakeDiscord) Send(ctx context.Context, channelID string, m core.Message) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.record("Send", channelID, m); err != nil {
		return "", err
	}
	d.nextID++
	return strconv.Itoa(d.nextID), nil
}

func (d *FakeDiscord) Edit(ctx context.Context, channelID, messageID string, m core.Message) error {
	return d.call("Edit", channelID, messageID, m)
}

func (d *FakeDiscord) Delete(ctx context.Context, channelID, messageID string) error {
	return d.call("Delete", channelID, messageID)
}

func (d *FakeDiscord) Pin(ctx context.Context, channelID, messageID string) error {
	return d.call("Pin", channelID, messageID)
}

func (d *FakeDiscord) DM(ctx context.Context, userID string, m core.Message) error {
	return d.call("DM", userID, m)
}

func (d *FakeDiscord) AddRole(ctx context.Context, userID, roleID string) error {
	return d.call("AddRole", userID, roleID)
}

func (d *FakeDiscord) RemoveRole(ctx context.Context, userID, roleID string) error {
	return d.call("RemoveRole", userID, roleID)
}

func (d *FakeDiscord) Respond(ctx context.Context, in *core.Interaction, m core.Message, ephemeral bool) error {
	return d.call("Respond", in, m, ephemeral)
}

func (d *FakeDiscord) Defer(ctx context.Context, in *core.Interaction, ephemeral bool) error {
	return d.call("Defer", in, ephemeral)
}

func (d *FakeDiscord) FollowUp(ctx context.Context, in *core.Interaction, m core.Message, ephemeral bool) error {
	return d.call("FollowUp", in, m, ephemeral)
}

func (d *FakeDiscord) Update(ctx context.Context, in *core.Interaction, m core.Message) error {
	return d.call("Update", in, m)
}

// ---- HolyArrs ----

// FakeHolyArrs returns whatever the test put in its fields. Set fields before
// the code under test starts, or change them later through Set. Errors are
// set per method name with SetErr and win over the values. Every call is
// recorded under its interface method name with the arguments after ctx.
type FakeHolyArrs struct {
	Recorder

	UsersVal    []core.User
	LinkCode    string
	LinkExpires time.Time
	// LinksVal is what Links returns; RemoveLink takes the user's entry out.
	LinksVal    []core.RemoteLink
	Recent      []core.Title // RecentlyAdded, cut to limit
	Titles      map[core.TitleID]core.Title
	PosterData  []byte
	PosterType  string
	ArtworkData map[string][]byte // Artwork, by ref
	PeopleVal   []core.PersonWatch
	WatchedVal  []core.WatchedTitle // RecentlyWatched, cut to limit
	UpcomingVal []core.CalendarItem
	// AskedByVal is what AskedBy returns, cut down to the ids asked about.
	AskedByVal map[core.TitleID][]core.UserID
	SearchVal  []core.SearchResult
	// Created is what CreateRequest returns; with an empty ID a pending
	// request is made up from the arguments.
	Created       core.Request
	CandidatesVal []core.CleanupCandidate
	Deleted       []core.TitleID // filled by DeleteTitle

	stream  *stream
	pages   []core.Page
	handler core.PageHandler
	widget  *core.Card
}

type stream struct {
	ch     chan core.Event
	closed bool
}

func NewFakeHolyArrs() *FakeHolyArrs {
	return &FakeHolyArrs{
		LinkCode:   "CODE-1234",
		Titles:     make(map[core.TitleID]core.Title),
		PosterType: "image/jpeg",
	}
}

// Set runs fn with the fake locked, for changing fields while the code under
// test is running.
func (h *FakeHolyArrs) Set(fn func(h *FakeHolyArrs)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *FakeHolyArrs) Users(ctx context.Context) ([]core.User, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Users"); err != nil {
		return nil, err
	}
	return append([]core.User(nil), h.UsersVal...), nil
}

func (h *FakeHolyArrs) NewLinkCode(ctx context.Context, external string) (string, time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("NewLinkCode", external); err != nil {
		return "", time.Time{}, err
	}
	return h.LinkCode, h.LinkExpires, nil
}

func (h *FakeHolyArrs) Links(ctx context.Context) ([]core.RemoteLink, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Links"); err != nil {
		return nil, err
	}
	return append([]core.RemoteLink(nil), h.LinksVal...), nil
}

// RemoveLink takes user's entry out of LinksVal, as HolyArrs would.
func (h *FakeHolyArrs) RemoveLink(ctx context.Context, user core.UserID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("RemoveLink", user); err != nil {
		return err
	}
	kept := make([]core.RemoteLink, 0, len(h.LinksVal))
	for _, l := range h.LinksVal {
		if l.UserID != user {
			kept = append(kept, l)
		}
	}
	h.LinksVal = kept
	return nil
}

func (h *FakeHolyArrs) RecentlyAdded(ctx context.Context, limit int) ([]core.Title, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("RecentlyAdded", limit); err != nil {
		return nil, err
	}
	out := append([]core.Title(nil), h.Recent...)
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Title returns Titles[id], or core.ErrNotFound when it is not there.
func (h *FakeHolyArrs) Title(ctx context.Context, id core.TitleID) (core.Title, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Title", id); err != nil {
		return core.Title{}, err
	}
	t, ok := h.Titles[id]
	if !ok {
		return core.Title{}, core.ErrNotFound
	}
	return t, nil
}

func (h *FakeHolyArrs) Poster(ctx context.Context, id core.TitleID) ([]byte, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Poster", id); err != nil {
		return nil, "", err
	}
	if h.PosterData == nil {
		return nil, "", core.ErrNotFound
	}
	return append([]byte(nil), h.PosterData...), h.PosterType, nil
}

// Artwork returns ArtworkData[ref], or core.ErrNotFound when it is not there.
func (h *FakeHolyArrs) Artwork(ctx context.Context, ref string) ([]byte, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Artwork", ref); err != nil {
		return nil, "", err
	}
	data, ok := h.ArtworkData[ref]
	if !ok {
		return nil, "", core.ErrNotFound
	}
	return append([]byte(nil), data...), "image/jpeg", nil
}

func (h *FakeHolyArrs) WatchPeople(ctx context.Context) ([]core.PersonWatch, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("WatchPeople"); err != nil {
		return nil, err
	}
	return append([]core.PersonWatch(nil), h.PeopleVal...), nil
}

func (h *FakeHolyArrs) RecentlyWatched(ctx context.Context, limit int) ([]core.WatchedTitle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("RecentlyWatched", limit); err != nil {
		return nil, err
	}
	out := append([]core.WatchedTitle(nil), h.WatchedVal...)
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (h *FakeHolyArrs) Upcoming(ctx context.Context, days int) ([]core.CalendarItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Upcoming", days); err != nil {
		return nil, err
	}
	return append([]core.CalendarItem(nil), h.UpcomingVal...), nil
}

func (h *FakeHolyArrs) AskedBy(ctx context.Context, ids []core.TitleID) (map[core.TitleID][]core.UserID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("AskedBy", append([]core.TitleID(nil), ids...)); err != nil {
		return nil, err
	}
	out := make(map[core.TitleID][]core.UserID)
	for _, id := range ids {
		if who, ok := h.AskedByVal[id]; ok {
			out[id] = append([]core.UserID(nil), who...)
		}
	}
	return out, nil
}

func (h *FakeHolyArrs) Search(ctx context.Context, query string) ([]core.SearchResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Search", query); err != nil {
		return nil, err
	}
	return append([]core.SearchResult(nil), h.SearchVal...), nil
}

func (h *FakeHolyArrs) CreateRequest(ctx context.Context, as core.UserID, what core.SearchResult) (core.Request, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("CreateRequest", as, what); err != nil {
		return core.Request{}, err
	}
	if h.Created.ID != "" {
		return h.Created, nil
	}
	return core.Request{
		ID:      "req-" + strconv.Itoa(len(h.calls)),
		Title:   core.Title{Name: what.Name, Year: what.Year, Kind: what.Kind, Overview: what.Overview, PosterURL: what.PosterURL},
		Status:  core.RequestPending,
		AskedBy: []core.UserID{as},
	}, nil
}

func (h *FakeHolyArrs) CleanupCandidates(ctx context.Context) ([]core.CleanupCandidate, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("CleanupCandidates"); err != nil {
		return nil, err
	}
	return append([]core.CleanupCandidate(nil), h.CandidatesVal...), nil
}

func (h *FakeHolyArrs) DeleteTitle(ctx context.Context, id core.TitleID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("DeleteTitle", id); err != nil {
		return err
	}
	h.Deleted = append(h.Deleted, id)
	return nil
}

// Events opens a new stream (buffer 64) that replaces the previous one. Feed
// it with Emit; it closes on CloseEvents or when ctx ends.
func (h *FakeHolyArrs) Events(ctx context.Context) (<-chan core.Event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("Events"); err != nil {
		return nil, err
	}
	s := &stream{ch: make(chan core.Event, 64)}
	h.stream = s
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		defer h.mu.Unlock()
		h.closeStream(s)
	}()
	return s.ch, nil
}

func (h *FakeHolyArrs) closeStream(s *stream) {
	if s != nil && !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// Emit puts e on the current event stream. It reports false when there is no
// open stream or its buffer is full.
func (h *FakeHolyArrs) Emit(e core.Event) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stream == nil || h.stream.closed {
		return false
	}
	select {
	case h.stream.ch <- e:
		return true
	default:
		return false
	}
}

// CloseEvents closes the current event stream, as a dropped connection would.
func (h *FakeHolyArrs) CloseEvents() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeStream(h.stream)
}

// ServePages records the pages and handler, then blocks until ctx ends.
func (h *FakeHolyArrs) ServePages(ctx context.Context, pages []core.Page, serve core.PageHandler) error {
	h.mu.Lock()
	err := h.record("ServePages", pages, serve)
	if err == nil {
		h.pages, h.handler = append([]core.Page(nil), pages...), serve
	}
	h.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// Pages returns the pages declared by the last ServePages call.
func (h *FakeHolyArrs) Pages() []core.Page {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]core.Page(nil), h.pages...)
}

// FetchPage asks the handler given to ServePages for a file.
func (h *FakeHolyArrs) FetchPage(file string) (body []byte, contentType string, ok bool) {
	h.mu.Lock()
	handler := h.handler
	h.mu.Unlock()
	if handler == nil {
		return nil, "", false
	}
	return handler(file)
}

// SetHomeWidget records the card as the published Home widget.
func (h *FakeHolyArrs) SetHomeWidget(ctx context.Context, c core.Card) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record("SetHomeWidget", c); err != nil {
		return err
	}
	c.Rows = append([]core.CardRow(nil), c.Rows...)
	h.widget = &c
	return nil
}

// HomeWidget returns the card from the last successful SetHomeWidget call.
func (h *FakeHolyArrs) HomeWidget() (core.Card, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.widget == nil {
		return core.Card{}, false
	}
	c := *h.widget
	c.Rows = append([]core.CardRow(nil), c.Rows...)
	return c, true
}

// ---- Deps ----

// Now is the fixed time NewDeps hands out.
var Now = time.Date(2026, time.January, 2, 15, 4, 5, 0, time.UTC)

// Fakes are the handles behind a Deps from NewDeps.
type Fakes struct {
	Store  *FakeStore
	Links  *FakeLinks
	Events *FakeEvents
	DC     *FakeDiscord
	HA     *FakeHolyArrs
}

// NewDeps returns a core.Deps wired to fresh fakes, with a filled-in Config,
// a fixed Now and a logger that discards everything. Tests may change the
// returned Deps (it is a value) before handing it to the code under test.
func NewDeps(t testing.TB) (core.Deps, *Fakes) {
	t.Helper()
	f := &Fakes{
		Store:  NewFakeStore(),
		Links:  NewFakeLinks(),
		Events: NewFakeEvents(),
		DC:     NewFakeDiscord(),
		HA:     NewFakeHolyArrs(),
	}
	t.Cleanup(f.Events.Close)
	return core.Deps{
		Cfg: core.Config{
			BotToken:         "test-bot-token",
			GuildID:          "100000000000000001",
			InviteURL:        "https://discord.gg/test",
			DashboardChannel: "100000000000000002",
			AnnounceChannel:  "100000000000000003",
			ModChannel:       "100000000000000004",
			AdminRoleID:      "100000000000000005",
			LinkedRoleID:     "100000000000000006",
			RefreshEvery:     5 * time.Minute,
			EnableDashboard:  true,
			EnableAnnounce:   true,
			EnableRequests:   true,
			EnableHomeWidget: true,
			DataDir:          "/nonexistent/coretest",
			HolyArrsTarget:   "http://holyarrs.invalid",
			HolyArrsToken:    "test-plugin-token",
		},
		HA:     f.HA,
		DC:     f.DC,
		Store:  f.Store,
		Links:  f.Links,
		Events: f.Events,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return Now },
	}, f
}
