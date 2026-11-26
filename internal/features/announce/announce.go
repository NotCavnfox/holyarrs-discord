// Package announce posts new library titles to the announce channel and
// tells people by DM when a title they asked for is in the library.
package announce

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	storeDoc        = "announced"
	maxKeys         = 500
	defaultWindow   = 60 * time.Second
	defaultBatchMax = 5
	overviewLen     = 350
	maxPosterBytes  = 8 << 20
	// maxQueue caps the events waiting for the worker. A replay burst of a
	// thousand events fits; beyond the cap the newest are dropped and logged.
	maxQueue = 2000
	colorNew = 0x57F287
)

type announcedDoc struct {
	Keys []string `json:"keys"`
}

// Feature is the announcer. The receive goroutine only moves events from the
// bus into the queue; everything else is owned by the Run goroutine (the
// worker), which is the only one that calls HolyArrs and Discord.
type Feature struct {
	d        core.Deps
	window   time.Duration // how long new titles are collected before posting
	batchMax int           // more titles than this in one window become one summary

	loaded     bool
	keys       []string
	seen       map[string]bool
	pending    []core.TitleID
	pendingSet map[core.TitleID]bool

	// events is subscribed in New so nothing published before Run is lost.
	events <-chan core.Event

	qmu     sync.Mutex
	inbox   []core.Event
	dropped int           // events lost since the queue was last below the cap
	wake    chan struct{} // tells the worker the queue is not empty
}

// New builds the announce feature.
func New(d core.Deps) *Feature {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	f := &Feature{
		d: d, window: defaultWindow, batchMax: defaultBatchMax,
		seen: map[string]bool{}, pendingSet: map[core.TitleID]bool{},
		wake: make(chan struct{}, 1),
	}
	if d.Cfg.EnableAnnounce && d.Events != nil {
		f.events = d.Events.Subscribe(f.Name())
	}
	return f
}

func (f *Feature) Name() string { return "announce" }

func (f *Feature) Commands() []core.Command { return nil }

// Run collects events until ctx ends. Events are taken off the bus at once
// and queued; the worker loop below handles them one at a time, so a burst
// never backs up into the bus while HolyArrs is being asked about a title.
func (f *Feature) Run(ctx context.Context) error {
	if f.events == nil {
		<-ctx.Done()
		return nil
	}
	f.load()
	received := make(chan struct{})
	go func() {
		defer close(received)
		f.receive(ctx)
	}()
	defer func() { <-received }()
	f.signal() // events queued before a restart

	var timer *time.Timer
	var flushC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			if len(f.pending) > 0 {
				// Do not lose what was already collected on shutdown.
				c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				f.flush(c)
				cancel()
			}
			return nil
		case <-f.wake:
			ev, ok := f.pop()
			if !ok {
				continue
			}
			if f.handle(ctx, ev) && flushC == nil {
				timer = time.NewTimer(f.window)
				flushC = timer.C
			}
		case <-flushC:
			flushC = nil
			f.flush(ctx)
		}
	}
}

// receive moves events from the bus into the queue until ctx ends or the bus
// closes. It never calls HolyArrs or Discord.
func (f *Feature) receive(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-f.events:
			if !ok {
				return
			}
			f.enqueue(ev)
		}
	}
}

// enqueue adds an event the worker cares about. Over maxQueue the event is
// dropped; that is logged once per overflow.
func (f *Feature) enqueue(ev core.Event) {
	if ev.Type != core.EventTitleAdded && ev.Type != core.EventRequestAvailable {
		return
	}
	f.qmu.Lock()
	if len(f.inbox) >= maxQueue {
		f.dropped++
		first := f.dropped == 1
		f.qmu.Unlock()
		if first {
			f.d.Log.Warn("announce: event queue is full, dropping events", "cap", maxQueue)
		}
		return
	}
	f.inbox = append(f.inbox, ev)
	f.qmu.Unlock()
	f.signal()
}

// pop takes the oldest queued event and wakes the worker again when more wait.
func (f *Feature) pop() (core.Event, bool) {
	f.qmu.Lock()
	if len(f.inbox) == 0 {
		f.qmu.Unlock()
		return core.Event{}, false
	}
	ev := f.inbox[0]
	f.inbox[0] = core.Event{}
	f.inbox = f.inbox[1:]
	more := len(f.inbox) > 0
	if !more {
		f.inbox = nil // let the burst's backing array go
	}
	dropped := f.dropped
	f.dropped = 0
	f.qmu.Unlock()
	if dropped > 0 {
		f.d.Log.Warn("announce: event queue has room again", "dropped", dropped)
	}
	if more {
		f.signal()
	}
	return ev, true
}

func (f *Feature) queued() int {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	return len(f.inbox)
}

func (f *Feature) signal() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// handle reacts to one event. It reports whether a title was queued, which
// means a flush is now owed.
func (f *Feature) handle(ctx context.Context, ev core.Event) bool {
	f.load()
	switch ev.Type {
	case core.EventTitleAdded:
		f.notifyReady(ctx, ev.TitleID)
		return f.queue(ev.TitleID)
	case core.EventRequestAvailable:
		f.notifyReady(ctx, ev.TitleID)
	}
	return false
}

func (f *Feature) load() {
	if f.loaded {
		return
	}
	f.loaded = true
	var doc announcedDoc
	if err := f.d.Store.Load(storeDoc, &doc); err != nil {
		f.d.Log.Warn("announce: load", "err", err)
		return
	}
	for _, k := range doc.Keys {
		f.mark(k)
	}
}

func (f *Feature) save() {
	if err := f.d.Store.Save(storeDoc, announcedDoc{Keys: f.keys}); err != nil {
		f.d.Log.Warn("announce: save", "err", err)
	}
}

// mark remembers a key, forgetting the oldest beyond maxKeys.
func (f *Feature) mark(key string) {
	if f.seen[key] {
		return
	}
	f.seen[key] = true
	f.keys = append(f.keys, key)
	if over := len(f.keys) - maxKeys; over > 0 {
		for _, k := range f.keys[:over] {
			delete(f.seen, k)
		}
		f.keys = append([]string(nil), f.keys[over:]...)
	}
}

func addedKey(id core.TitleID) string { return fmt.Sprintf("added:%d", id) }

func (f *Feature) queue(id core.TitleID) bool {
	if f.d.Cfg.AnnounceChannel == "" || id == 0 || f.seen[addedKey(id)] || f.pendingSet[id] {
		return false
	}
	f.pendingSet[id] = true
	f.pending = append(f.pending, id)
	return true
}

// flush posts everything collected in the window: one embed per title, or
// one summary when there are more than batchMax.
func (f *Feature) flush(ctx context.Context) {
	ids := f.pending
	f.pending = nil
	f.pendingSet = map[core.TitleID]bool{}
	if len(ids) == 0 {
		return
	}
	titles := make([]core.Title, 0, len(ids))
	for _, id := range ids {
		t, err := f.d.HA.Title(ctx, id)
		if err != nil {
			f.d.Log.Warn("announce: title lookup", "title", int64(id), "err", err)
			continue
		}
		if t.ID == 0 {
			t.ID = id
		}
		titles = append(titles, t)
	}
	if len(titles) == 0 {
		return
	}
	ch := f.d.Cfg.AnnounceChannel
	if len(titles) > f.batchMax {
		if _, err := f.d.DC.Send(ctx, ch, summaryMessage(titles, f.d.Now())); err != nil {
			f.d.Log.Warn("announce: post summary", "err", err)
			return
		}
		for _, t := range titles {
			f.mark(addedKey(t.ID))
		}
		f.save()
		return
	}
	posted := false
	for _, t := range titles {
		if _, err := f.d.DC.Send(ctx, ch, f.titleMessage(ctx, t)); err != nil {
			f.d.Log.Warn("announce: post title", "title", int64(t.ID), "err", err)
			continue
		}
		f.mark(addedKey(t.ID))
		posted = true
	}
	if posted {
		f.save()
	}
}

func plainTitle(name string, year int) string {
	s := safeName(name)
	if year > 0 {
		s += fmt.Sprintf(" (%d)", year)
	}
	return s
}

func (f *Feature) titleMessage(ctx context.Context, t core.Title) core.Message {
	e := core.Embed{
		Title:       plainTitle(t.Name, t.Year),
		Description: escapeMD(truncate(strings.Join(strings.Fields(t.Overview), " "), overviewLen)),
		Color:       colorNew,
		Footer:      "New in the library",
		Time:        t.AddedAt,
	}
	if e.Time.IsZero() {
		e.Time = f.d.Now()
	}
	if t.Year > 0 {
		e.Fields = append(e.Fields, core.Field{Name: "Year", Value: fmt.Sprint(t.Year), Inline: true})
	}
	if k := kindLabel(t.Kind); k != "" {
		e.Fields = append(e.Fields, core.Field{Name: "Kind", Value: k, Inline: true})
	}
	m := core.Message{}
	if strings.HasPrefix(t.PosterURL, "https://") {
		e.Thumbnail = t.PosterURL
	} else if data, ct, err := f.d.HA.Poster(ctx, t.ID); err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			f.d.Log.Debug("announce: poster", "title", int64(t.ID), "err", err)
		}
	} else if len(data) > 0 && len(data) <= maxPosterBytes {
		name := "poster.jpg"
		switch {
		case strings.HasPrefix(ct, "image/png"):
			name = "poster.png"
		case strings.HasPrefix(ct, "image/webp"):
			name = "poster.webp"
		}
		m.Files = []core.File{{Name: name, ContentType: ct, Data: data}}
		e.Thumbnail = "attachment://" + name
	}
	m.Embeds = []core.Embed{e}
	return clampMessage(m)
}

func summaryMessage(titles []core.Title, now time.Time) core.Message {
	lines := make([]string, 0, len(titles))
	for _, t := range titles {
		l := "• " + titleLabel(t.Name, t.Year)
		if k := kindLabel(t.Kind); k != "" {
			l += " · " + k
		}
		lines = append(lines, l)
	}
	e := core.Embed{
		Title:       fmt.Sprintf("%d new titles in the library", len(titles)),
		Description: joinLines(lines, maxEmbedDesc),
		Color:       colorNew,
		Time:        now,
	}
	return clampMessage(core.Message{Embeds: []core.Embed{e}})
}

func readyKey(id core.TitleID, discordID string) string {
	return fmt.Sprintf("ready:%d:%s", id, discordID)
}

// notifyReady DMs every linked person who asked for a title that is now in
// the library and has alerts on, once per title and member. HolyArrs gives a plugin no list of
// requests, so this works from the title: an event without a title id is
// ignored.
func (f *Feature) notifyReady(ctx context.Context, id core.TitleID) {
	if id == 0 {
		return
	}
	asked, err := f.d.HA.AskedBy(ctx, []core.TitleID{id})
	if err != nil {
		f.d.Log.Warn("announce: asked-by lookup", "title", int64(id), "err", err)
		return
	}
	var to []string
	told := map[string]bool{}
	for _, uid := range asked[id] {
		link, ok := f.d.Links.ByUser(uid)
		// AlertsOff is the member's own choice (/privacy alerts:off).
		if !ok || link.AlertsOff || link.DiscordID == "" || told[link.DiscordID] || f.seen[readyKey(id, link.DiscordID)] {
			continue
		}
		told[link.DiscordID] = true
		to = append(to, link.DiscordID)
	}
	if len(to) == 0 {
		return
	}
	label := "A title you asked for"
	t, err := f.d.HA.Title(ctx, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return // not in the library after all: a later event will carry it
	case err != nil:
		f.d.Log.Warn("announce: title lookup", "title", int64(id), "err", err)
	case t.Name != "":
		label = titleLabel(t.Name, t.Year)
	}
	msg := core.Message{Content: truncate(label+" is now available.", 2000)}
	changed := false
	for _, discordID := range to {
		err := f.d.DC.DM(ctx, discordID, msg)
		if err != nil && !errors.Is(err, core.ErrForbidden) {
			f.d.Log.Warn("announce: dm", "title", int64(id), "err", err)
			continue
		}
		// Sent, or DMs are closed: either way do not try again.
		f.mark(readyKey(id, discordID))
		changed = true
	}
	if changed {
		f.save()
	}
}
