// Package dashboard keeps one pinned message per tile in the dashboard
// channel and edits it in place when its content changes.
package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	storeDoc        = "tiles"
	tileRows        = 10
	upcomingDays    = 14
	defaultRefresh  = 5 * time.Minute
	defaultDebounce = 10 * time.Second
	colorTile       = 0x5865F2
)

// Tile keys, in the order the messages are created.
const (
	tileAdded    = "added"
	tileWatched  = "watched"
	tileTop      = "top"
	tileUpcoming = "upcoming"
)

type tileState struct {
	MessageID string `json:"message_id"`
	Hash      string `json:"hash"`
}

type tilesDoc struct {
	Channel string               `json:"channel"`
	Tiles   map[string]tileState `json:"tiles"`
}

// Feature is the dashboard.
type Feature struct {
	d        core.Deps
	debounce time.Duration

	mu     sync.Mutex // one refresh at a time; guards doc
	doc    tilesDoc
	loaded bool

	// events is subscribed in New so nothing published before Run is lost.
	events <-chan core.Event
}

// New builds the dashboard feature.
func New(d core.Deps) *Feature {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	f := &Feature{d: d, debounce: defaultDebounce}
	if f.enabled() && d.Events != nil {
		f.events = d.Events.Subscribe(f.Name())
	}
	return f
}

func (f *Feature) Name() string { return "dashboard" }

func (f *Feature) enabled() bool {
	return f.d.Cfg.EnableDashboard && f.d.Cfg.DashboardChannel != ""
}

// Commands returns /dashboard refresh (admins only) when the dashboard is on.
func (f *Feature) Commands() []core.Command {
	if !f.enabled() {
		return nil
	}
	return []core.Command{{
		Name:        "dashboard",
		Description: "Manage the dashboard",
		AdminOnly:   true,
		Sub: []core.Command{{
			Name:        "refresh",
			Description: "Refresh the dashboard tiles now",
			AdminOnly:   true,
			Handle:      f.handleRefresh,
		}},
	}}
}

func (f *Feature) handleRefresh(ctx context.Context, in *core.Interaction) error {
	if !in.IsAdmin {
		return f.d.DC.Respond(ctx, in, core.Message{Content: "Only admins can refresh the dashboard."}, true)
	}
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		return err
	}
	text := "Dashboard refreshed."
	if err := f.Refresh(ctx); err != nil {
		f.d.Log.Warn("dashboard: manual refresh", "err", err)
		text = "Some tiles could not be refreshed. Details are in the log."
	}
	return f.d.DC.FollowUp(ctx, in, core.Message{Content: text}, true)
}

// Run refreshes on a timer and, debounced, on library and request events.
func (f *Feature) Run(ctx context.Context) error {
	if !f.enabled() {
		<-ctx.Done()
		return nil
	}
	every := f.d.Cfg.RefreshEvery
	if every <= 0 {
		every = defaultRefresh
	}
	events := f.events
	refresh := func() {
		if err := f.Refresh(ctx); err != nil && ctx.Err() == nil {
			f.d.Log.Warn("dashboard: refresh", "err", err)
		}
	}
	refresh()
	tick := time.NewTicker(every)
	defer tick.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			refresh()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if relevant(ev.Type) && debounceC == nil {
				debounce = time.NewTimer(f.debounce)
				debounceC = debounce.C
			}
		case <-debounceC:
			debounceC = nil
			refresh()
		}
	}
}

func relevant(t core.EventType) bool {
	switch t {
	case core.EventTitleAdded, core.EventRequestCreated, core.EventRequestApproved,
		core.EventRequestAvailable, core.EventRequestDeclined:
		return true
	}
	return false
}

func (f *Feature) load() {
	if f.loaded {
		return
	}
	f.loaded = true
	var doc tilesDoc
	if err := f.d.Store.Load(storeDoc, &doc); err != nil {
		f.d.Log.Warn("dashboard: load tiles", "err", err)
		doc = tilesDoc{}
	}
	ch := f.d.Cfg.DashboardChannel
	if doc.Tiles == nil || doc.Channel != ch {
		// A new channel means the old message ids are useless.
		doc = tilesDoc{Channel: ch, Tiles: map[string]tileState{}}
	}
	f.doc = doc
}

// Refresh renders every tile and sends, edits or skips each one. A tile that
// fails is reported in the returned error; the others still update.
func (f *Feature) Refresh(ctx context.Context) error {
	if !f.enabled() {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	now := f.d.Now()

	tiles := []struct {
		key    string
		render func() (core.Embed, error)
	}{
		{tileAdded, func() (core.Embed, error) {
			ts, err := f.d.HA.RecentlyAdded(ctx, tileRows)
			if err != nil {
				return core.Embed{}, err
			}
			return renderAdded(ts), nil
		}},
		{tileWatched, func() (core.Embed, error) {
			ws, err := f.d.HA.RecentlyWatched(ctx, tileRows)
			if err != nil {
				return core.Embed{}, err
			}
			return renderWatched(ws), nil
		}},
		{tileTop, func() (core.Embed, error) {
			ps, err := f.d.HA.WatchPeople(ctx)
			if err != nil {
				return core.Embed{}, err
			}
			return renderTop(ps, f.d.Links), nil
		}},
		{tileUpcoming, func() (core.Embed, error) {
			cs, err := f.d.HA.Upcoming(ctx, upcomingDays)
			if err != nil {
				return core.Embed{}, err
			}
			return renderUpcoming(cs, now), nil
		}},
	}

	var errs []error
	changed := false
	for _, t := range tiles {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		e, err := t.render()
		if err != nil {
			f.d.Log.Warn("dashboard: render tile", "tile", t.key, "err", err)
			errs = append(errs, fmt.Errorf("tile %s: %w", t.key, err))
			continue
		}
		e.Color = colorTile
		e.Footer = "Updated"
		e.Time = time.Time{}
		msg := core.Message{Embeds: []core.Embed{clampEmbed(e)}}
		h := hashMessage(msg) // before Time is set: the clock must not force an edit
		msg.Embeds[0].Time = now
		did, err := f.sync(ctx, t.key, msg, h)
		if err != nil {
			f.d.Log.Warn("dashboard: update tile", "tile", t.key, "err", err)
			errs = append(errs, fmt.Errorf("tile %s: %w", t.key, err))
		}
		changed = changed || did
	}
	if changed {
		if err := f.d.Store.Save(storeDoc, f.doc); err != nil {
			f.d.Log.Warn("dashboard: save tiles", "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sync makes the channel show msg for one tile. It reports whether the saved
// state changed.
func (f *Feature) sync(ctx context.Context, key string, msg core.Message, hash string) (bool, error) {
	ch := f.d.Cfg.DashboardChannel
	st := f.doc.Tiles[key]
	if st.MessageID != "" {
		if st.Hash == hash {
			return false, nil
		}
		err := f.d.DC.Edit(ctx, ch, st.MessageID, msg)
		if err == nil {
			st.Hash = hash
			f.doc.Tiles[key] = st
			return true, nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return false, err
		}
		// The message was deleted: fall through and make a new one.
	}
	id, err := f.d.DC.Send(ctx, ch, msg)
	if err != nil {
		return false, err
	}
	f.doc.Tiles[key] = tileState{MessageID: id, Hash: hash}
	if err := f.d.DC.Pin(ctx, ch, id); err != nil {
		f.d.Log.Warn("dashboard: pin tile", "tile", key, "err", err)
	}
	return true, nil
}

func hashMessage(m core.Message) string {
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func body(lines []string) string {
	if len(lines) == 0 {
		return "Nothing here yet."
	}
	return joinLines(lines, maxEmbedDesc)
}

func withKind(s string, k core.Kind) string {
	if l := kindLabel(k); l != "" {
		return s + " · " + l
	}
	return s
}

func renderAdded(ts []core.Title) core.Embed {
	ts = append([]core.Title(nil), ts...)
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].AddedAt.After(ts[j].AddedAt) })
	if len(ts) > tileRows {
		ts = ts[:tileRows]
	}
	e := core.Embed{Title: "Recently added"}
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
		l := withKind(titleLabel(t.Name, t.Year), t.Kind)
		if !t.AddedAt.IsZero() {
			l += " · " + relTime(t.AddedAt)
		}
		lines = append(lines, l)
	}
	if len(ts) > 0 && strings.HasPrefix(ts[0].PosterURL, "https://") {
		e.Thumbnail = ts[0].PosterURL
	}
	e.Description = body(lines)
	return e
}

// renderWatched lists titles and when they were last watched. HolyArrs does
// not say who watched what, and neither does this tile.
func renderWatched(ws []core.WatchedTitle) core.Embed {
	ws = append([]core.WatchedTitle(nil), ws...)
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].LastWatched.After(ws[j].LastWatched) })
	if len(ws) > tileRows {
		ws = ws[:tileRows]
	}
	lines := make([]string, 0, len(ws))
	for _, w := range ws {
		l := titleLabel(w.Title.Name, w.Title.Year)
		if !w.LastWatched.IsZero() {
			l += " · " + relTime(w.LastWatched)
		}
		lines = append(lines, l)
	}
	e := core.Embed{Title: "Recently watched", Description: "Nothing yet"}
	if len(lines) > 0 {
		e.Description = joinLines(lines, maxEmbedDesc)
	}
	return e
}

// mentionFor returns the Discord mention to show for a watcher, or "" when
// the person has not opted in. No user id, no link, or a link without
// ShowWatching all mean no name.
func mentionFor(user core.UserID, links core.Links) string {
	if user == "" || links == nil {
		return ""
	}
	l, ok := links.ByUser(user)
	if !ok || !l.ShowWatching || l.DiscordID == "" {
		return ""
	}
	for _, r := range l.DiscordID {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "<@" + l.DiscordID + ">"
}

func plays(n int) string {
	if n == 1 {
		return "1 play"
	}
	return fmt.Sprintf("%d plays", n)
}

// minFoldTotal is how many unnamed people the closing line must cover
// before it shows their combined plays.
const minFoldTotal = 3

// renderTop ranks people by plays. Only the top tileRows are candidates for a
// row, and only those who opted in get one, as a Discord mention. Everyone
// else is counted in a single closing line without a name.
func renderTop(ps []core.PersonWatch, links core.Links) core.Embed {
	type row struct {
		user  core.UserID
		plays int
	}
	idx := map[core.UserID]int{}
	var rows []row
	for _, p := range ps {
		if p.Plays <= 0 {
			continue
		}
		if p.UserID != "" { // one person can have several media-server accounts
			if i, ok := idx[p.UserID]; ok {
				rows[i].plays += p.Plays
				continue
			}
			idx[p.UserID] = len(rows)
		}
		rows = append(rows, row{p.UserID, p.Plays})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].plays > rows[j].plays })

	var lines []string
	others, otherPlays := 0, 0
	for i, r := range rows {
		m := ""
		if i < tileRows {
			m = mentionFor(r.user, links)
		}
		if m == "" {
			others++
			otherPlays += r.plays
			continue
		}
		lines = append(lines, fmt.Sprintf("%d\\. %s · %s", len(lines)+1, m, plays(r.plays)))
	}
	if others > 0 {
		who := "others"
		switch {
		case len(lines) == 0 && others == 1:
			who = "person"
		case len(lines) == 0:
			who = "people"
		case others == 1:
			who = "other"
		}
		l := fmt.Sprintf("%d %s", others, who)
		// A total over one or two people is as good as naming them: with
		// the named rows next to it, it gives away what each one watched.
		if others >= minFoldTotal {
			l += ", " + plays(otherPlays)
		}
		if len(lines) > 0 {
			l = "and " + l
		}
		lines = append(lines, l)
	}
	e := core.Embed{Title: "Top watchers", Description: "Nothing yet"}
	if len(lines) > 0 {
		e.Description = joinLines(lines, maxEmbedDesc)
	}
	return e
}

func renderUpcoming(cs []core.CalendarItem, now time.Time) core.Embed {
	cs = append([]core.CalendarItem(nil), cs...)
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Date.Before(cs[j].Date) })
	// Keep things releasing today even if the hour has passed.
	cutoff := now.Add(-12 * time.Hour)
	var lines []string
	for _, c := range cs {
		if len(lines) == tileRows {
			break
		}
		if c.Date.Before(cutoff) {
			continue
		}
		l := titleLabel(c.Title.Name, c.Title.Year)
		if c.Label != "" {
			l += " · " + safeName(c.Label)
		}
		l += " · " + relTime(c.Date)
		lines = append(lines, l)
	}
	return core.Embed{Title: "Coming up", Description: body(lines)}
}
