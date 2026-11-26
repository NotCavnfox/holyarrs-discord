// Package requests owns /me, /search, /request and /privacy, the "Request"
// buttons under search results, and the record of requests made through the
// bot (HolyArrs gives a plugin no list of requests).
package requests

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	featureName  = "requests"
	actionReq    = "req"
	maxResults   = 5
	maxMine      = 15
	storeDoc     = "myrequests"
	maxStored    = 50 // requests remembered per member
	defaultTTL   = 15 * time.Minute
	defaultCache = 500
	colorInfo    = 0x5865F2

	textNotLinked = "You haven't linked your HolyArrs account yet. Run /link to connect it, then try again."
	textLimit     = "You've reached your request limit."
	textExpired   = "That search has expired. Search again with /search."
	textNotYours  = "Only the member who ran this search can use its buttons."
	textDisabled  = "Requests are turned off on this server."
	textTrouble   = "Something went wrong talking to HolyArrs. Try again in a bit."
	textInLibrary = "That's already in the library."
	textNotListed = "Requests made in HolyArrs itself are not listed here."
)

// myRequest is one request a member made through the bot.
type myRequest struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Year    int                `json:"year,omitempty"`
	Kind    core.Kind          `json:"kind,omitempty"`
	TitleID core.TitleID       `json:"title_id,omitempty"`
	Status  core.RequestStatus `json:"status"`
	At      time.Time          `json:"at"`
}

type cacheEntry struct {
	owner   string // Discord id of the member who searched
	result  core.SearchResult
	expires time.Time
}

// Feature is the requests feature.
type Feature struct {
	d        core.Deps
	ttl      time.Duration
	maxCache int
	newKey   func() string

	mu    sync.Mutex
	cache map[string]cacheEntry

	recMu  sync.Mutex
	loaded bool
	mine   map[string][]myRequest // Discord id -> newest first

	// events is subscribed in New so nothing published before Run is lost.
	events <-chan core.Event
}

// New builds the requests feature.
func New(d core.Deps) *Feature {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	f := &Feature{d: d, ttl: defaultTTL, maxCache: defaultCache, newKey: randomKey, cache: map[string]cacheEntry{}}
	if d.Events != nil {
		f.events = d.Events.Subscribe(f.Name())
	}
	return f
}

func randomKey() string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (f *Feature) Name() string { return featureName }

// Run keeps the stored requests up to date from request events.
func (f *Feature) Run(ctx context.Context) error {
	if f.events == nil {
		<-ctx.Done()
		return nil
	}
	events := f.events
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				<-ctx.Done()
				return nil
			}
			f.handleEvent(ctx, ev)
		}
	}
}

// ---- the members' own requests ----

// loadLocked reads the document once. Callers hold f.recMu.
func (f *Feature) loadLocked() {
	if f.loaded {
		return
	}
	f.loaded = true
	f.mine = map[string][]myRequest{}
	if f.d.Store == nil {
		return
	}
	var doc map[string][]myRequest
	if err := f.d.Store.Load(storeDoc, &doc); err != nil {
		f.d.Log.Warn("requests: load "+storeDoc, "err", err)
		return
	}
	for id, list := range doc {
		if id != "" && len(list) > 0 {
			f.mine[id] = list
		}
	}
}

// saveLocked writes the document. Callers hold f.recMu.
func (f *Feature) saveLocked() {
	if f.d.Store == nil {
		return
	}
	if err := f.d.Store.Save(storeDoc, f.mine); err != nil {
		f.d.Log.Warn("requests: save "+storeDoc, "err", err)
	}
}

// remember records a request the member just made, newest first. Asking
// again for something already recorded updates that entry.
func (f *Feature) remember(discordID string, r myRequest) {
	f.recMu.Lock()
	defer f.recMu.Unlock()
	f.loadLocked()
	list := f.mine[discordID]
	for i, old := range list {
		if (r.ID != "" && old.ID == r.ID) || (r.ID == "" && old.ID == "" && r.TitleID != 0 && old.TitleID == r.TitleID) {
			if r.TitleID == 0 {
				r.TitleID = old.TitleID
			}
			r.At = old.At
			list[i] = r
			f.saveLocked()
			return
		}
	}
	list = append([]myRequest{r}, list...)
	if len(list) > maxStored {
		list = list[:maxStored]
	}
	f.mine[discordID] = list
	f.saveLocked()
}

// requestsOf returns a copy of the member's stored requests, newest first.
func (f *Feature) requestsOf(discordID string) []myRequest {
	f.recMu.Lock()
	defer f.recMu.Unlock()
	f.loadLocked()
	return append([]myRequest(nil), f.mine[discordID]...)
}

// matches reports whether an event is about a stored request: by request id
// when both carry one, else by title id.
func matches(r myRequest, ev core.Event) bool {
	if ev.RequestID != "" && r.ID != "" {
		return r.ID == ev.RequestID
	}
	return ev.TitleID != 0 && r.TitleID == ev.TitleID
}

type statusChange struct {
	discordID string
	req       myRequest
}

// handleEvent updates stored requests from an approved, declined or
// available event and DMs the member on approved and declined. The "ready"
// DM belongs to the announce feature.
func (f *Feature) handleEvent(ctx context.Context, ev core.Event) {
	var to core.RequestStatus
	switch ev.Type {
	case core.EventRequestApproved:
		to = core.RequestApproved
	case core.EventRequestDeclined:
		to = core.RequestDeclined
	case core.EventRequestAvailable:
		to = core.RequestAvailable
	default:
		return
	}
	if ev.RequestID == "" && ev.TitleID == 0 {
		return
	}
	var changed []statusChange
	f.recMu.Lock()
	f.loadLocked()
	dirty := false
	for discordID, list := range f.mine {
		for i, r := range list {
			if !matches(r, ev) {
				continue
			}
			if r.TitleID == 0 && ev.TitleID != 0 {
				list[i].TitleID = ev.TitleID
				dirty = true
			}
			// An event replayed after a restart must not undo a later state.
			if r.Status == to || (to == core.RequestApproved && r.Status == core.RequestAvailable) {
				continue
			}
			list[i].Status = to
			dirty = true
			changed = append(changed, statusChange{discordID, list[i]})
		}
	}
	if dirty {
		f.saveLocked()
	}
	f.recMu.Unlock()

	if to == core.RequestAvailable {
		return
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].discordID < changed[j].discordID })
	for _, c := range changed {
		what := "was approved."
		if to == core.RequestDeclined {
			what = "was declined."
		}
		label := "a title"
		if c.req.Name != "" {
			label = titleLabel(c.req.Name, c.req.Year)
		}
		err := f.d.DC.DM(ctx, c.discordID, text(truncate("Your request for "+label+" "+what, 2000)))
		if err != nil && !errors.Is(err, core.ErrForbidden) {
			f.d.Log.Warn("requests: dm", "request", c.req.ID, "err", err)
		}
	}
}

// Commands returns /me and /privacy always, /search and /request when
// requests are enabled.
func (f *Feature) Commands() []core.Command {
	cmds := []core.Command{
		{Name: "me", Description: "Your requests and watch totals", Handle: f.handleMe},
		{
			Name: "privacy", Description: "Choose whether your name shows next to what you watch",
			Options: []core.Option{{Name: "show_watching", Description: "Show my name next to what I watch", Type: core.OptBool, Required: true}},
			Handle:  f.handlePrivacy,
		},
	}
	if !f.d.Cfg.EnableRequests {
		return cmds
	}
	return append(cmds,
		core.Command{
			Name: "search", Description: "Search for a title to request",
			Options: []core.Option{{Name: "query", Description: "What to look for", Type: core.OptString, Required: true}},
			Handle:  func(ctx context.Context, in *core.Interaction) error { return f.handleSearch(ctx, in, "query", false) },
		},
		core.Command{
			Name: "request", Description: "Request a title",
			Options: []core.Option{{Name: "title", Description: "The title you want", Type: core.OptString, Required: true}},
			Handle:  func(ctx context.Context, in *core.Interaction) error { return f.handleSearch(ctx, in, "title", true) },
		},
	)
}

func text(s string) core.Message { return core.Message{Content: s} }

// errText turns an error from HolyArrs into something to show a member.
func errText(err error) string {
	switch {
	case errors.Is(err, core.ErrAlreadyAvailable):
		return textInLibrary
	case errors.Is(err, core.ErrLimit):
		return textLimit
	case errors.Is(err, core.ErrNotLinked):
		return textNotLinked
	case errors.Is(err, core.ErrForbidden):
		return "HolyArrs didn't allow that."
	case errors.Is(err, core.ErrRateLimited):
		return "HolyArrs is busy right now. Try again in a minute."
	}
	return textTrouble
}

// ---- /privacy ----

func (f *Feature) handlePrivacy(ctx context.Context, in *core.Interaction) error {
	link, ok := f.d.Links.ByDiscord(in.UserID)
	if !ok {
		return f.d.DC.Respond(ctx, in, text(textNotLinked), true)
	}
	show, err := strconv.ParseBool(in.Options["show_watching"])
	if err != nil {
		return f.d.DC.Respond(ctx, in, text("Choose true or false for show_watching."), true)
	}
	link.ShowWatching = show
	if err := f.d.Links.Put(link); err != nil {
		f.d.Log.Error("requests: save privacy", "err", err)
		return f.d.DC.Respond(ctx, in, text("Couldn't save that. Try again in a bit."), true)
	}
	msg := "Done. Your name will no longer appear next to what you watch."
	if show {
		msg = "Done. Your name will now appear next to what you watch."
	}
	return f.d.DC.Respond(ctx, in, text(msg), true)
}

// ---- /me ----

var statusOrder = []struct {
	s     core.RequestStatus
	label string
}{
	{core.RequestPending, "Pending"},
	{core.RequestApproved, "Approved"},
	{core.RequestAvailable, "Available"},
	{core.RequestDeclined, "Declined"},
	{core.RequestOther, "Other"},
}

func (f *Feature) handleMe(ctx context.Context, in *core.Interaction) error {
	link, ok := f.d.Links.ByDiscord(in.UserID)
	if !ok {
		return f.d.DC.Respond(ctx, in, text(textNotLinked), true)
	}
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		return err
	}
	people, werr := f.d.HA.WatchPeople(ctx)
	if werr != nil {
		f.d.Log.Warn("requests: /me watch totals", "err", werr)
	}
	e := meEmbed(link, f.requestsOf(in.UserID), people, werr != nil)
	return f.d.DC.FollowUp(ctx, in, clampMessage(core.Message{Embeds: []core.Embed{e}}), true)
}

func meEmbed(link core.Link, mine []myRequest, people []core.PersonWatch, watchFailed bool) core.Embed {
	total := len(mine)
	if len(mine) > maxMine {
		mine = mine[:maxMine]
	}
	e := core.Embed{Title: "Your HolyArrs", Color: colorInfo}
	if link.UserName != "" {
		e.Title = truncate("Your HolyArrs · "+escapeMD(link.UserName), maxEmbedTitle)
	}
	known := map[core.RequestStatus]bool{}
	for _, so := range statusOrder {
		known[so.s] = true
	}
	for _, so := range statusOrder {
		var lines []string
		for _, r := range mine {
			st := r.Status
			if !known[st] {
				st = core.RequestOther
			}
			if st != so.s {
				continue
			}
			l := titleLabel(r.Name, r.Year)
			if k := kindLabel(r.Kind); k != "" {
				l += " · " + k
			}
			if !r.At.IsZero() {
				l += " · " + relTime(r.At)
			}
			lines = append(lines, l)
		}
		if len(lines) == 0 {
			continue
		}
		e.Fields = append(e.Fields, core.Field{
			Name:  fmt.Sprintf("%s (%d)", so.label, len(lines)),
			Value: joinLines(lines, maxFieldValue),
		})
	}
	switch {
	case total == 0:
		e.Description = "You have made no requests through the bot yet. Use /request to ask for something. " + textNotListed
	case total > maxMine:
		e.Description = fmt.Sprintf("Your %d newest requests of %d made through the bot. ", maxMine, total) + textNotListed
	default:
		e.Description = "Your requests made through the bot. " + textNotListed
	}

	// Their own totals: this is the caller's data, shown only to them. One
	// person can have several media-server accounts.
	var sum core.PersonWatch
	found := false
	for _, p := range people {
		if p.UserID == "" || p.UserID != link.UserID {
			continue
		}
		found = true
		sum.Movies += p.Movies
		sum.Shows += p.Shows
		sum.Episodes += p.Episodes
		sum.Plays += p.Plays
		if p.LastWatched.After(sum.LastWatched) {
			sum.LastWatched = p.LastWatched
		}
	}
	watch := "Nothing watched yet."
	switch {
	case found:
		watch = fmt.Sprintf("Movies: %d · Shows: %d · Episodes: %d · Plays: %d", sum.Movies, sum.Shows, sum.Episodes, sum.Plays)
		if !sum.LastWatched.IsZero() {
			watch += "\nLast watched " + relTime(sum.LastWatched)
		}
	case watchFailed:
		watch = "Couldn't load this right now."
	}
	e.Fields = append(e.Fields, core.Field{Name: "Your watching", Value: watch})
	return e
}

// ---- /search and /request ----

func (f *Feature) handleSearch(ctx context.Context, in *core.Interaction, opt string, confirmSingle bool) error {
	if !f.d.Cfg.EnableRequests {
		return f.d.DC.Respond(ctx, in, text(textDisabled), true)
	}
	query := strings.TrimSpace(in.Options[opt])
	if query == "" {
		return f.d.DC.Respond(ctx, in, text("Tell me what to look for."), true)
	}
	if _, ok := f.d.Links.ByDiscord(in.UserID); !ok {
		return f.d.DC.Respond(ctx, in, text(textNotLinked), true)
	}
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		return err
	}
	results, err := f.d.HA.Search(ctx, query)
	if err != nil {
		f.d.Log.Warn("requests: search", "err", err)
		return f.d.DC.FollowUp(ctx, in, text(errText(err)), true)
	}
	if len(results) == 0 {
		return f.d.DC.FollowUp(ctx, in, text("Nothing found for "+truncate(escapeMD(query), 200)+"."), true)
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return f.d.DC.FollowUp(ctx, in, f.resultsMessage(in.UserID, query, results, confirmSingle), true)
}

func (f *Feature) resultsMessage(owner, query string, results []core.SearchResult, confirmSingle bool) core.Message {
	if confirmSingle && len(results) == 1 && !results[0].InLibrary {
		r := results[0]
		e := core.Embed{
			Title:       truncate("Request "+plainResult(r)+"?", maxEmbedTitle),
			Description: escapeMD(truncate(strings.Join(strings.Fields(r.Overview), " "), 300)),
			Color:       colorInfo,
		}
		if strings.HasPrefix(r.PosterURL, "https://") {
			e.Thumbnail = r.PosterURL
		}
		return clampMessage(core.Message{
			Embeds: []core.Embed{e},
			Rows: [][]core.Button{{{
				Label: "Confirm request", Style: core.ButtonSuccess,
				CustomID: f.customID(f.put(owner, r)),
			}}},
		})
	}
	var lines []string
	var row []core.Button
	for i, r := range results {
		l := fmt.Sprintf("**%d.** %s", i+1, titleLabel(r.Name, r.Year))
		if k := kindLabel(r.Kind); k != "" {
			l += " · " + k
		}
		if r.InLibrary {
			l += " · already in the library"
		} else {
			row = append(row, core.Button{
				Label:    truncate(fmt.Sprintf("Request %d: %s", i+1, strings.Join(strings.Fields(r.Name), " ")), 80),
				Style:    core.ButtonPrimary,
				CustomID: f.customID(f.put(owner, r)),
			})
		}
		if ov := strings.Join(strings.Fields(r.Overview), " "); ov != "" {
			l += "\n" + escapeMD(truncate(ov, 140))
		}
		lines = append(lines, l)
	}
	m := core.Message{Embeds: []core.Embed{{
		Title:       truncate("Results for "+escapeMD(truncate(query, 100)), maxEmbedTitle),
		Description: joinLines(lines, maxEmbedDesc),
		Color:       colorInfo,
		Footer:      "Buttons work for 15 minutes.",
	}}}
	if len(row) > 0 {
		m.Rows = [][]core.Button{row}
	} else {
		m.Embeds[0].Footer = ""
	}
	return clampMessage(m)
}

func plainResult(r core.SearchResult) string {
	s := safeName(r.Name)
	if r.Year > 0 {
		s += fmt.Sprintf(" (%d)", r.Year)
	}
	return s
}

func (f *Feature) customID(key string) string {
	return featureName + ":" + actionReq + ":" + key
}

// ---- result cache ----

// put stores a result for owner and returns its key.
func (f *Feature) put(owner string, r core.SearchResult) string {
	now := f.d.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, e := range f.cache {
		if !e.expires.After(now) {
			delete(f.cache, k)
		}
	}
	for len(f.cache) >= f.maxCache && len(f.cache) > 0 {
		oldest, first := "", true
		var at time.Time
		for k, e := range f.cache {
			if first || e.expires.Before(at) {
				oldest, at, first = k, e.expires, false
			}
		}
		delete(f.cache, oldest)
	}
	key := f.newKey()
	f.cache[key] = cacheEntry{owner: owner, result: r, expires: now.Add(f.ttl)}
	return key
}

type takeResult int

const (
	takeOK takeResult = iota
	takeExpired
	takeNotOwner
)

// take removes and returns the entry when it is live and belongs to caller.
// Removing it makes a double press harmless.
func (f *Feature) take(key, caller string) (cacheEntry, takeResult) {
	now := f.d.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[key]
	if !ok {
		return cacheEntry{}, takeExpired
	}
	if !e.expires.After(now) {
		delete(f.cache, key)
		return cacheEntry{}, takeExpired
	}
	if e.owner != caller {
		return cacheEntry{}, takeNotOwner
	}
	delete(f.cache, key)
	return e, takeOK
}

// restore puts an entry back after a failure the member can retry.
func (f *Feature) restore(key string, e cacheEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cache) < f.maxCache {
		f.cache[key] = e
	}
}

// ---- buttons ----

// HandleComponent handles a press on a "Request" button.
func (f *Feature) HandleComponent(ctx context.Context, in *core.Interaction) error {
	parts := strings.SplitN(in.CustomID, ":", 3)
	if len(parts) != 3 || parts[0] != featureName || parts[1] != actionReq || parts[2] == "" {
		return f.d.DC.Respond(ctx, in, text("That button isn't in use any more."), true)
	}
	if !f.d.Cfg.EnableRequests {
		return f.d.DC.Respond(ctx, in, text(textDisabled), true)
	}
	key := parts[2]
	entry, res := f.take(key, in.UserID)
	switch res {
	case takeExpired:
		return f.d.DC.Respond(ctx, in, text(textExpired), true)
	case takeNotOwner:
		return f.d.DC.Respond(ctx, in, text(textNotYours), true)
	}
	link, ok := f.d.Links.ByDiscord(in.UserID)
	if !ok {
		f.restore(key, entry)
		return f.d.DC.Respond(ctx, in, text(textNotLinked), true)
	}
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		f.restore(key, entry)
		return err
	}
	req, err := f.d.HA.CreateRequest(ctx, link.UserID, entry.result)
	if err != nil {
		if errors.Is(err, core.ErrAlreadyAvailable) {
			// Nothing to retry and nothing to record: it is there to watch.
			return f.d.DC.FollowUp(ctx, in, text(textInLibrary), true)
		}
		if !errors.Is(err, core.ErrLimit) && !errors.Is(err, core.ErrNotLinked) {
			f.d.Log.Warn("requests: create request", "err", err)
		}
		f.restore(key, entry)
		return f.d.DC.FollowUp(ctx, in, text(errText(err)), true)
	}
	rec := myRequest{
		ID: req.ID, Name: req.Title.Name, Year: req.Title.Year, Kind: req.Title.Kind,
		TitleID: req.Title.ID, Status: req.Status, At: req.CreatedAt,
	}
	if rec.Name == "" {
		rec.Name, rec.Year = entry.result.Name, entry.result.Year
	}
	if rec.Kind == "" {
		rec.Kind = entry.result.Kind
	}
	if rec.Status == "" {
		rec.Status = core.RequestPending
	}
	if rec.At.IsZero() {
		rec.At = f.d.Now()
	}
	f.remember(in.UserID, rec)
	label := titleLabel(entry.result.Name, entry.result.Year)
	var msg string
	switch req.Status {
	case core.RequestAvailable:
		msg = label + " is already available."
	case core.RequestApproved:
		msg = "Requested " + label + ". It was approved straight away."
	case core.RequestDeclined:
		msg = "Your request for " + label + " was declined."
	default:
		msg = "Requested " + label + ". It is waiting for approval."
	}
	return f.d.DC.FollowUp(ctx, in, text(msg), true)
}

var _ core.ComponentFeature = (*Feature)(nil)
