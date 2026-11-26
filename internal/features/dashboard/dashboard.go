// Package dashboard keeps one pinned message per tile in the dashboard
// channel and edits it in place when its content changes. Recently added and
// Coming up carry a 3x3 poster grid drawn here as one attached picture.
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
	"github.com/NotCavnfox/holyarrs-discord/internal/postergrid"
)

const (
	storeDoc        = "tiles"
	tileRows        = 10
	upcomingDays    = 14
	defaultRefresh  = 5 * time.Minute
	defaultDebounce = 10 * time.Second
	colorTile       = 0x5865F2

	gridCells      = postergrid.Cells // titles on a poster-grid tile
	posterFetchers = 3                // poster reads at once
	posterTimeout  = 20 * time.Second // per poster read
	maxPosterBytes = 1 << 20          // larger pictures are not drawn; a small poster is under 100 KB
	// posterRetries is how many later refreshes try again when a poster read
	// failed for a reason that may pass. After that the tile stays as it is
	// until its content changes.
	posterRetries = 3
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
	// Hash covers everything the tile shows: its text and, for a poster
	// grid, the title ids and poster references in grid order.
	Hash string `json:"hash"`
	// Retries counts the refreshes that drew this content with a poster
	// missing because its read failed.
	Retries int `json:"retries,omitempty"`
}

// gridItem is one cell of a poster-grid tile.
type gridItem struct {
	ID      core.TitleID `json:"id"`
	Title   string       `json:"title"`
	Artwork string       `json:"artwork"` // core.Title.Artwork; empty: no stored poster
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
	// Forced: every tile is drawn and sent again, so a tile message that was
	// deleted in Discord comes back even when its content did not change.
	if err := f.refresh(ctx, true); err != nil {
		f.d.Log.Warn("dashboard: manual refresh", "err", err)
		text = "Some tiles could not be refreshed. Details are in the log."
	}
	return f.d.DC.FollowUp(ctx, in, core.Message{Content: text}, true)
}

// Run refreshes on a timer and, debounced, on library, request and link
// events (a link change can add or remove a name on Top watchers).
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
		core.EventRequestAvailable, core.EventRequestDeclined,
		core.EventLinkChanged, core.EventLinkRemoved:
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
func (f *Feature) Refresh(ctx context.Context) error { return f.refresh(ctx, false) }

// refresh is Refresh; with force no tile is skipped for being unchanged.
func (f *Feature) refresh(ctx context.Context, force bool) error {
	if !f.enabled() {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	now := f.d.Now()

	tiles := []struct {
		key    string
		render func() (core.Embed, []gridItem, error)
	}{
		{tileAdded, func() (core.Embed, []gridItem, error) {
			ts, err := f.d.HA.RecentlyAdded(ctx, gridCells)
			if err != nil {
				return core.Embed{}, nil, err
			}
			e, grid := renderAdded(ts)
			return e, grid, nil
		}},
		{tileWatched, func() (core.Embed, []gridItem, error) {
			ws, err := f.d.HA.RecentlyWatched(ctx, tileRows)
			if err != nil {
				return core.Embed{}, nil, err
			}
			return renderWatched(ws), nil, nil
		}},
		{tileTop, func() (core.Embed, []gridItem, error) {
			ps, err := f.d.HA.WatchPeople(ctx)
			if err != nil {
				return core.Embed{}, nil, err
			}
			return renderTop(ps, f.d.Links), nil, nil
		}},
		{tileUpcoming, func() (core.Embed, []gridItem, error) {
			cs, err := f.d.HA.Upcoming(ctx, upcomingDays)
			if err != nil {
				return core.Embed{}, nil, err
			}
			e, grid := renderUpcoming(cs, now)
			return e, grid, nil
		}},
	}

	var errs []error
	changed := false
	for _, t := range tiles {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		e, grid, err := t.render()
		if err != nil {
			f.d.Log.Warn("dashboard: render tile", "tile", t.key, "err", err)
			errs = append(errs, fmt.Errorf("tile %s: %w", t.key, err))
			continue
		}
		e.Color = colorTile
		e.Footer = "Updated"
		e.Time = time.Time{}
		file := t.key + ".jpg"
		if len(grid) > 0 {
			e.Image = "attachment://" + file
		}
		msg := core.Message{Embeds: []core.Embed{clampEmbed(e)}}
		h := hashTile(msg, grid) // before Time is set: the clock must not force an edit
		st := f.doc.Tiles[t.key]
		if !force && st.MessageID != "" && st.Hash == h && (st.Retries == 0 || st.Retries >= posterRetries) {
			continue // nothing changed: no poster reads, no upload
		}
		retries := 0
		if len(grid) > 0 {
			jpg, failed, err := f.composeGrid(ctx, t.key, grid)
			switch {
			case ctx.Err() != nil:
				errs = append(errs, ctx.Err())
				continue
			case err != nil:
				// The list still says everything; post it without the picture.
				f.d.Log.Warn("dashboard: draw tile", "tile", t.key, "err", err)
				msg.Embeds[0].Image = ""
			default:
				msg.Files = []core.File{{Name: file, ContentType: "image/jpeg", Data: jpg}}
			}
			if failed > 0 || err != nil {
				retries = 1
				if st.Hash == h {
					retries = st.Retries + 1
				}
			}
		}
		msg.Embeds[0].Time = now
		if err := f.sync(ctx, t.key, msg, h, retries); err != nil {
			f.d.Log.Warn("dashboard: update tile", "tile", t.key, "err", err)
			errs = append(errs, fmt.Errorf("tile %s: %w", t.key, err))
			continue
		}
		changed = true
	}
	if changed {
		if err := f.d.Store.Save(storeDoc, f.doc); err != nil {
			f.d.Log.Warn("dashboard: save tiles", "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// composeGrid reads the posters of a grid tile at HolyArrs's small size, at
// most posterFetchers at once, and draws them as one JPEG. Only the small
// encoded pictures are held together; postergrid decodes one at a time. A title with no stored poster, or whose
// poster cannot be read or used, gets a cell with its name. failed counts the
// reads that failed for a reason that may pass (not "no such picture" and not
// "no permission"), so the caller can try again later.
// errPosterTooBig is a poster over maxPosterBytes. It wraps core.ErrNotFound
// so it is not read again: trying again will not help.
var errPosterTooBig = fmt.Errorf("poster over the size cap: %w", core.ErrNotFound)

func (f *Feature) composeGrid(ctx context.Context, key string, grid []gridItem) (jpg []byte, failed int, err error) {
	if len(grid) > gridCells {
		grid = grid[:gridCells]
	}
	// One read per picture, even when several episodes share a poster.
	type result struct {
		data []byte
		err  error
	}
	got := map[string]*result{}
	var refs []string
	for _, g := range grid {
		if g.Artwork != "" && got[g.Artwork] == nil {
			got[g.Artwork] = &result{}
			refs = append(refs, g.Artwork)
		}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, posterFetchers)
	for _, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				got[ref].err = ctx.Err()
				return
			}
			defer func() { <-slots }()
			c, cancel := context.WithTimeout(ctx, posterTimeout)
			defer cancel()
			data, _, err := f.d.HA.Thumbnail(c, ref)
			if err == nil && len(data) > maxPosterBytes {
				data, err = nil, errPosterTooBig // too large to draw; trying again will not help
			}
			got[ref].data, got[ref].err = data, err
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	cells := make([]postergrid.Cell, len(grid))
	want, denied := 0, 0
	why := map[string]int{} // why a stored poster was not drawn; reasons only, no names
	for i, g := range grid {
		cells[i].Title = g.Title
		r := got[g.Artwork]
		if r == nil {
			continue
		}
		want++
		switch {
		case r.err == nil:
			cells[i].Poster = r.data
		case errors.Is(r.err, core.ErrForbidden):
			denied++
			why["not permitted"]++
		case errors.Is(r.err, errPosterTooBig):
			why["over the size cap"]++
		case errors.Is(r.err, core.ErrNotFound):
			why["not found"]++
		default:
			failed++
			why["read failed"]++
		}
	}
	jpg, drawn, unusable, err := postergrid.ComposeDetail(cells)
	if err != nil {
		return nil, failed, err
	}
	for _, u := range unusable {
		why[u]++
	}
	if denied > 0 {
		f.d.Log.Warn("dashboard: posters need the library.artwork.read permission", "tile", key, "posters", denied)
	}
	f.d.Log.Info("dashboard: drew tile", "tile", key, "titles", len(grid), "stored", want, "drawn", drawn, "failed", failed, "bytes", len(jpg))
	if len(why) > 0 {
		reasons := make([]string, 0, len(why))
		for r, n := range why {
			reasons = append(reasons, fmt.Sprintf("%dx %s", n, r))
		}
		sort.Strings(reasons)
		f.d.Log.Warn("dashboard: posters not drawn", "tile", key, "why", strings.Join(reasons, "; "))
	}
	return jpg, failed, nil
}

// sync makes the channel show msg for one tile and notes its hash. An edit
// replaces the message's old attachment with the one in msg.
func (f *Feature) sync(ctx context.Context, key string, msg core.Message, hash string, retries int) error {
	ch := f.d.Cfg.DashboardChannel
	st := f.doc.Tiles[key]
	if st.MessageID != "" {
		err := f.d.DC.Edit(ctx, ch, st.MessageID, msg)
		if err == nil {
			st.Hash, st.Retries = hash, retries
			f.doc.Tiles[key] = st
			return nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return err
		}
		// The message was deleted: fall through and make a new one.
	}
	id, err := f.d.DC.Send(ctx, ch, msg)
	if err != nil {
		return err
	}
	f.doc.Tiles[key] = tileState{MessageID: id, Hash: hash, Retries: retries}
	if err := f.d.DC.Pin(ctx, ch, id); err != nil {
		f.d.Log.Warn("dashboard: pin tile", "tile", key, "err", err)
	}
	return nil
}

// hashTile is the identity of what a tile shows. The picture itself is left
// out: it follows from the grid, and drawing it is what the hash avoids.
func hashTile(m core.Message, grid []gridItem) string {
	b, _ := json.Marshal(struct {
		Msg  core.Message
		Grid []gridItem
	}{m, grid})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func body(lines []string) string {
	if len(lines) == 0 {
		return "Nothing here yet."
	}
	return joinLines(lines, maxEmbedDesc)
}

// renderAdded is the Recently added tile: the gridCells newest titles as a
// numbered list, and the same titles in the same order for the poster grid.
func renderAdded(ts []core.Title) (core.Embed, []gridItem) {
	ts = append([]core.Title(nil), ts...)
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].AddedAt.After(ts[j].AddedAt) })
	if len(ts) > gridCells {
		ts = ts[:gridCells]
	}
	lines := make([]string, 0, len(ts))
	grid := make([]gridItem, 0, len(ts))
	for i, t := range ts {
		lines = append(lines, fmt.Sprintf("%d\\. %s", i+1, titleLabel(t.Name, t.Year)))
		grid = append(grid, gridItem{ID: t.ID, Title: t.Name, Artwork: t.Artwork})
	}
	return core.Embed{Title: "Recently added", Description: body(lines)}, grid
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

// renderUpcoming is the Coming up tile: the next gridCells releases as a
// numbered list with their dates, and the same order for the poster grid.
func renderUpcoming(cs []core.CalendarItem, now time.Time) (core.Embed, []gridItem) {
	cs = append([]core.CalendarItem(nil), cs...)
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Date.Before(cs[j].Date) })
	// Keep things releasing today even if the hour has passed.
	cutoff := now.Add(-12 * time.Hour)
	var lines []string
	var grid []gridItem
	for _, c := range cs {
		if len(lines) == gridCells {
			break
		}
		if c.Date.Before(cutoff) {
			continue
		}
		l := fmt.Sprintf("%d\\. %s", len(lines)+1, titleLabel(c.Title.Name, c.Title.Year))
		if c.Label != "" {
			l += " · " + safeName(c.Label)
		}
		l += " · " + relTime(c.Date)
		lines = append(lines, l)
		grid = append(grid, gridItem{ID: c.Title.ID, Title: c.Title.Name, Artwork: c.Title.Artwork})
	}
	return core.Embed{Title: "Coming up", Description: body(lines)}, grid
}
