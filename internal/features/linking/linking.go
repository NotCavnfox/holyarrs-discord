// Package linking ties Discord members to HolyArrs users: /link, /unlink,
// /whoami, /link-admin, the reconcile loop that follows the links HolyArrs
// holds, the Linked role, the plugin page and the Home widget.
package linking

import (
	"context"
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
	pendingDoc      = "pending"
	defaultCodeLife = 10 * time.Minute
	// listMax keeps /link-admin list inside one Discord message (2000 characters).
	listMax = 1800

	whereToEnter = "My account → Linked plugins"

	textAdminOnly   = "Only admins can use this command."
	textUnsupported = "Linking needs a newer HolyArrs. Ask the server owner to update."
)

// pendingEntry is a member who was shown a link code and has not been seen
// linked yet. The code itself is never kept. An entry decides nothing about
// who gets linked: it only makes the reconcile loop look more often.
type pendingEntry struct {
	DiscordID string    `json:"discord_id"`
	Expires   time.Time `json:"expires"`
}

type pendingFile struct {
	Pending []pendingEntry `json:"pending"`
}

// support is what is known about the HolyArrs link routes.
type support int

const (
	supportUnknown support = iota
	supportYes
	supportNo
)

// Feature implements core.ComponentFeature.
type Feature struct {
	d core.Deps

	// FastEvery is how often the links in HolyArrs are read while a code is
	// waiting to be typed; SlowEvery is how often otherwise. PageRetry is the
	// first backoff after the page server stops. Set them before Run.
	FastEvery time.Duration
	SlowEvery time.Duration
	PageRetry time.Duration
	// WidgetRetry is the first wait after a failed Home widget publish; it
	// doubles up to widgetMaxRetry.
	WidgetRetry time.Duration

	page []byte

	// Home widget state, used by Run's goroutine only.
	widgetSent  int // link count on the published card; -1 = not published
	widgetWait  time.Duration
	widgetAfter time.Time

	warned bool          // the "HolyArrs is too old" warning was logged; Run's goroutine only
	wake   chan struct{} // a command changed something the loop should look at

	// opMu makes a reconcile pass and an unlink take turns, so a pass never
	// acts on a list that was read before the unlink.
	opMu sync.Mutex

	mu      sync.Mutex
	loaded  bool
	pending map[string]time.Time // Discord id -> when the code expires
	support support
}

// New builds the linking feature.
func New(d core.Deps) *Feature {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Feature{
		d:           d,
		FastEvery:   5 * time.Second,
		SlowEvery:   5 * time.Minute,
		PageRetry:   time.Second,
		WidgetRetry: 5 * time.Second,
		widgetSent:  -1,
		page:        renderPage(d.Cfg.InviteURL),
		wake:        make(chan struct{}, 1),
		pending:     map[string]time.Time{},
	}
}

func (f *Feature) Name() string { return "linking" }

func (f *Feature) Commands() []core.Command {
	return []core.Command{
		{Name: "link", Description: "Link your Discord account to your HolyArrs user", Handle: f.handleLink},
		{Name: "unlink", Description: "Remove your HolyArrs link", Handle: f.handleUnlink},
		{Name: "whoami", Description: "Show which HolyArrs user you are linked to", Handle: f.handleWhoami},
		{
			Name:        "link-admin",
			Description: "Manage links",
			AdminOnly:   true,
			Sub: []core.Command{
				{
					Name:        "list",
					Description: "Show who is linked",
					AdminOnly:   true,
					Handle:      f.handleAdminList,
				},
				{
					Name:        "remove",
					Description: "Remove a member's HolyArrs link",
					AdminOnly:   true,
					Options: []core.Option{{
						Name:        "member",
						Description: "The member to unlink",
						Type:        core.OptUser,
						Required:    true,
					}},
					Handle: f.handleAdminRemove,
				},
			},
		},
	}
}

// HandleComponent: this feature owns no buttons yet.
func (f *Feature) HandleComponent(_ context.Context, in *core.Interaction) error {
	id := ""
	if in != nil {
		id = in.CustomID
	}
	return fmt.Errorf("linking: unknown component %q", id)
}

// Run serves the plugin page, keeps the Home widget current and follows the
// links HolyArrs holds until ctx ends: one pass at start, then every
// FastEvery while a code is waiting and every SlowEvery otherwise.
func (f *Feature) Run(ctx context.Context) error {
	if err := f.load(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.servePages(ctx)
	}()
	defer wg.Wait()

	f.widgetSent = -1 // HolyArrs forgot the card when the plugin last stopped
	f.warned = false
	next := time.Now()
	for {
		if now := time.Now(); !now.Before(next) {
			f.reconcile(ctx)
			next = time.Now().Add(f.cadence())
		} else if soon := now.Add(f.cadence()); soon.Before(next) {
			next = soon // a code was just issued: do not sit out the slow wait
		}
		f.syncWidget(ctx)

		wait := time.Until(next)
		if !f.widgetAfter.IsZero() {
			wait = min(wait, time.Until(f.widgetAfter))
		}
		t := time.NewTimer(max(wait, time.Millisecond))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-f.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// cadence is the wait before the next pass.
func (f *Feature) cadence() time.Duration {
	now := f.d.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.support == supportNo {
		return f.SlowEvery
	}
	for _, exp := range f.pending {
		if now.Before(exp) {
			return f.FastEvery
		}
	}
	return f.SlowEvery
}

// poke makes Run look again now instead of after its wait.
func (f *Feature) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *Feature) servePages(ctx context.Context) {
	const maxBackoff = time.Minute
	backoff := f.PageRetry
	for ctx.Err() == nil {
		err := f.d.HA.ServePages(ctx, f.Pages(), f.ServePage)
		if ctx.Err() != nil {
			return
		}
		f.d.Log.Warn("linking: page server stopped, retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// ---- pending book ----

// load reads pending.json once. Entries made before it ran are kept.
func (f *Feature) load() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loaded {
		return nil
	}
	var doc pendingFile
	if err := f.d.Store.Load(pendingDoc, &doc); err != nil {
		return fmt.Errorf("linking: load pending: %w", err)
	}
	for _, e := range doc.Pending {
		if e.DiscordID == "" {
			continue
		}
		if _, ok := f.pending[e.DiscordID]; !ok {
			f.pending[e.DiscordID] = e.Expires
		}
	}
	f.loaded = true
	return nil
}

// saveLocked writes the pending book; a failure is logged and the change
// stands in memory. Losing the file only costs the fast cadence after a
// restart. Callers hold f.mu.
func (f *Feature) saveLocked() {
	doc := pendingFile{Pending: make([]pendingEntry, 0, len(f.pending))}
	for id, exp := range f.pending {
		doc.Pending = append(doc.Pending, pendingEntry{DiscordID: id, Expires: exp})
	}
	sort.Slice(doc.Pending, func(i, j int) bool { return doc.Pending[i].DiscordID < doc.Pending[j].DiscordID })
	if err := f.d.Store.Save(pendingDoc, doc); err != nil {
		f.d.Log.Error("linking: pending book not saved", "err", err)
	}
}

func (f *Feature) setPending(discordID string, expires time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[discordID] = expires
	f.saveLocked()
}

// dropPending forgets a member's waiting code and reports whether one was
// still waiting.
func (f *Feature) dropPending(discordID string) bool {
	now := f.d.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	exp, ok := f.pending[discordID]
	if !ok {
		return false
	}
	delete(f.pending, discordID)
	f.saveLocked()
	return now.Before(exp)
}

func (f *Feature) dropExpired() {
	now := f.d.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := false
	for id, exp := range f.pending {
		if !now.Before(exp) {
			delete(f.pending, id)
			changed = true
		}
	}
	if changed {
		f.saveLocked()
	}
}

func (f *Feature) pendingFor(discordID string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	exp, ok := f.pending[discordID]
	return exp, ok
}

func (f *Feature) setSupport(s support) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.support = s
}

// ---- reconcile ----

// reconcile reads the links HolyArrs holds and makes the bot match them.
// Nothing is changed unless the list was read: an error, or a HolyArrs
// without the route, never removes a link.
func (f *Feature) reconcile(ctx context.Context) {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	f.dropExpired()
	remote, err := f.d.HA.Links(ctx)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return
	case errors.Is(err, core.ErrUnsupported):
		f.setSupport(supportNo)
		if !f.warned {
			f.warned = true
			f.d.Log.Warn("linking: this HolyArrs cannot list links, so /link and /unlink are off until it is updated", "err", err)
		}
		return
	default:
		f.d.Log.Warn("linking: links not read from HolyArrs, nothing changed", "err", err)
		return
	}
	f.setSupport(supportYes)
	f.warned = false
	f.apply(ctx, remote)
}

// apply makes the link book match remote, the full list from HolyArrs.
func (f *Feature) apply(ctx context.Context, remote []core.RemoteLink) {
	// Group by Discord id. HolyArrs keeps one link per user, so two users can
	// hold the same Discord id; the bot keeps one user per Discord id.
	groups := map[string][]core.RemoteLink{}
	for _, r := range remote {
		if snowflake(r.External) {
			groups[r.External] = append(groups[r.External], r)
		}
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	winners := make(map[string]core.RemoteLink, len(groups))
	for _, id := range ids {
		g := groups[id]
		win := 0
		local, linked := f.d.Links.ByDiscord(id)
		kept := false
		for i, r := range g {
			if linked && r.UserID == local.UserID {
				win, kept = i, true
				break
			}
		}
		if !kept {
			for i := range g {
				if older(g[i], g[win]) {
					win = i
				}
			}
		}
		winners[id] = g[win]
		for i, r := range g {
			if i == win || ctx.Err() != nil {
				continue
			}
			if err := f.d.HA.RemoveLink(ctx, r.UserID); err != nil {
				f.d.Log.Warn("linking: duplicate link not removed in HolyArrs", "err", err)
				continue
			}
			f.d.Log.Info("linking: removed a second HolyArrs user linked to one Discord member", "linked", len(f.d.Links.All()))
		}
	}

	// Local links HolyArrs no longer holds.
	for _, l := range f.d.Links.All() {
		if w, ok := winners[l.DiscordID]; ok && w.UserID == l.UserID {
			continue
		}
		if err := f.d.Links.Remove(l.DiscordID); err != nil {
			f.d.Log.Error("linking: remove stale link", "err", err)
			continue
		}
		f.removeRole(ctx, l.DiscordID)
		f.d.Log.Info("linking: link removed, gone in HolyArrs", "linked", len(f.d.Links.All()))
	}

	// Links HolyArrs holds that the bot does not know yet. The external id can
	// only have come from a code this plugin asked for, for that Discord id.
	var names map[core.UserID]string
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, linked := f.d.Links.ByDiscord(id); linked {
			continue
		}
		w := winners[id]
		if names == nil {
			users, err := f.d.HA.Users(ctx)
			if err != nil {
				f.d.Log.Warn("linking: names not read from HolyArrs, new links wait for the next pass", "err", err)
				return
			}
			names = make(map[core.UserID]string, len(users))
			for _, u := range users {
				names[u.ID] = u.Name
			}
		}
		f.adopt(ctx, id, w, names[w.UserID])
	}
}

// adopt stores a link HolyArrs holds and tells the member.
func (f *Feature) adopt(ctx context.Context, discordID string, r core.RemoteLink, name string) {
	if strings.TrimSpace(name) == "" {
		name = string(r.UserID)
	}
	l := core.Link{
		DiscordID:    discordID,
		UserID:       r.UserID,
		UserName:     name,
		ShowWatching: false,
		LinkedAt:     f.d.Now(),
	}
	if err := f.d.Links.Put(l); err != nil {
		f.d.Log.Error("linking: save link", "err", err)
		return
	}
	f.dropPending(discordID)
	if role := f.d.Cfg.LinkedRoleID; role != "" {
		if err := f.d.DC.AddRole(ctx, discordID, role); err != nil {
			f.d.Log.Warn("linking: add role", "err", err)
		}
	}
	f.d.Log.Info("linking: linked", "linked", len(f.d.Links.All()))
	f.dm(ctx, discordID, "You are now linked to HolyArrs as **"+escapeMarkdown(name)+
		"**. Run /whoami to check, /privacy to choose whether your name shows next to what you watch, or /unlink to undo it.\n"+
		"I will message you here when a request of yours is approved, declined or available. /privacy alerts:off turns these messages off.")
}

// older reports whether a should be kept over b: the earlier link, and the
// lower user id when the times are the same.
func older(a, b core.RemoteLink) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	x, errX := strconv.ParseInt(string(a.UserID), 10, 64)
	y, errY := strconv.ParseInt(string(b.UserID), 10, 64)
	if errX != nil || errY != nil {
		return a.UserID < b.UserID
	}
	return x < y
}

// snowflake reports whether s could be a Discord id: 15 to 21 digits.
func snowflake(s string) bool {
	if len(s) < 15 || len(s) > 21 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (f *Feature) removeRole(ctx context.Context, discordID string) {
	role := f.d.Cfg.LinkedRoleID
	if role == "" {
		return
	}
	if err := f.d.DC.RemoveRole(ctx, discordID, role); err != nil {
		f.d.Log.Warn("linking: remove role", "err", err)
	}
}

func (f *Feature) dm(ctx context.Context, discordID, text string) {
	err := f.d.DC.DM(ctx, discordID, core.Message{Content: text})
	if err != nil && !errors.Is(err, core.ErrForbidden) {
		f.d.Log.Warn("linking: dm", "err", err)
	}
}

// ---- commands ----

func (f *Feature) handleLink(ctx context.Context, in *core.Interaction) error {
	// HolyArrs calls can take longer than Discord's 3 seconds.
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		return err
	}
	text, err := f.link(ctx, in)
	if err != nil {
		f.d.Log.Error("linking: /link", "err", err)
		text = "Something went wrong on my side. Try /link again in a minute."
	}
	return f.d.DC.FollowUp(ctx, in, core.Message{Content: text}, true)
}

func (f *Feature) link(ctx context.Context, in *core.Interaction) (string, error) {
	if err := f.load(); err != nil {
		return "", err
	}
	if l, ok := f.d.Links.ByDiscord(in.UserID); ok {
		return "You are already linked to **" + escapeMarkdown(l.UserName) +
			"**. Run /unlink first if you want to link a different HolyArrs user.", nil
	}
	if !snowflake(in.UserID) {
		return "", errors.New("linking: the caller has no usable Discord user id")
	}
	if text := f.checkSupport(ctx); text != "" {
		return text, nil
	}
	code, expires, err := f.newCode(ctx, in.UserID)
	if err != nil {
		if errors.Is(err, core.ErrRateLimited) || errors.Is(err, core.ErrForbidden) {
			return haDown(err), nil
		}
		return "", err
	}
	f.setPending(in.UserID, expires)
	f.poke()
	return "Your link code: `" + code + "`\n" +
		"It expires " + relTime(expires) + ".\n\n" +
		"1. Sign in to HolyArrs.\n" +
		"2. Open " + whereToEnter + " and type this code.\n" +
		"3. I will message you here on Discord once the link is confirmed.\n\n" +
		"Keep the code to yourself.", nil
}

// checkSupport returns the reply to give when a link could not be seen
// afterwards: HolyArrs has no route to list links, or it cannot be asked.
// It is "" when linking can go ahead.
func (f *Feature) checkSupport(ctx context.Context) string {
	f.mu.Lock()
	s := f.support
	f.mu.Unlock()
	if s == supportYes {
		return ""
	}
	// Not known yet, or too old at the last look (it may have been updated).
	if _, err := f.d.HA.Links(ctx); err != nil {
		if errors.Is(err, core.ErrUnsupported) {
			f.setSupport(supportNo)
			return textUnsupported
		}
		return haDown(err)
	}
	f.setSupport(supportYes)
	return ""
}

// newCode asks HolyArrs for a code and tidies the answer.
func (f *Feature) newCode(ctx context.Context, discordID string) (code string, expires time.Time, err error) {
	code, expires, err = f.d.HA.NewLinkCode(ctx, discordID)
	if err != nil {
		return "", time.Time{}, err
	}
	code = strings.TrimSpace(strings.ReplaceAll(code, "`", ""))
	if code == "" {
		return "", time.Time{}, errors.New("linking: HolyArrs returned an empty link code")
	}
	if expires.IsZero() {
		expires = f.d.Now().Add(defaultCodeLife)
	}
	return code, expires, nil
}

// unlinkResult is how an unlink went.
type unlinkResult int

const (
	unlinkDone unlinkResult = iota
	unlinkNotLinked
	unlinkHadPending // not linked, but a code was waiting
	unlinkUnsupported
	unlinkFailed
)

// unlink removes a member's link in HolyArrs first and only then in the bot,
// so a failure leaves both sides as they were. The caller removes the role
// after answering.
func (f *Feature) unlink(ctx context.Context, discordID, by string) (core.Link, unlinkResult) {
	if err := f.load(); err != nil {
		f.d.Log.Error("linking: unlink", "err", err)
	}
	f.opMu.Lock()
	defer f.opMu.Unlock()
	hadPending := f.dropPending(discordID)
	l, ok := f.d.Links.ByDiscord(discordID)
	if !ok {
		if hadPending {
			return l, unlinkHadPending
		}
		return l, unlinkNotLinked
	}
	if err := f.d.HA.RemoveLink(ctx, l.UserID); err != nil {
		if errors.Is(err, core.ErrUnsupported) {
			f.setSupport(supportNo)
			return l, unlinkUnsupported
		}
		f.d.Log.Error("linking: unlink: not removed in HolyArrs", "err", err)
		return l, unlinkFailed
	}
	if err := f.d.Links.Remove(discordID); err != nil {
		// Gone in HolyArrs, still here: the next reconcile pass removes it.
		f.d.Log.Error("linking: unlink: not removed here", "err", err)
		return l, unlinkFailed
	}
	f.d.Log.Info("linking: unlinked", "by_admin", by != discordID, "linked", len(f.d.Links.All()))
	f.poke()
	return l, unlinkDone
}

func (f *Feature) handleUnlink(ctx context.Context, in *core.Interaction) error {
	// The HolyArrs call can take longer than Discord's 3 seconds.
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		return err
	}
	l, res := f.unlink(ctx, in.UserID, in.UserID)
	var text string
	switch res {
	case unlinkDone:
		text = "You are no longer linked to **" + escapeMarkdown(l.UserName) + "**, here and in HolyArrs."
	case unlinkHadPending:
		text = "You were not linked yet. I stopped waiting for your link code. " +
			"If you still type it in HolyArrs before it expires, you will be linked."
	case unlinkNotLinked:
		text = "You are not linked. Run /link to link."
	case unlinkUnsupported:
		text = textUnsupported
	default:
		text = "I could not remove your link. Try /unlink again in a minute."
	}
	// Answer first: the role call can be slow.
	err := f.d.DC.FollowUp(ctx, in, core.Message{Content: text}, true)
	if res == unlinkDone {
		f.removeRole(ctx, in.UserID)
	}
	return err
}

// ---- admin commands ----

func (f *Feature) handleAdminList(ctx context.Context, in *core.Interaction) error {
	reply := func(text string) error {
		return f.d.DC.Respond(ctx, in, core.Message{Content: text}, true)
	}
	if !in.IsAdmin {
		return reply(textAdminOnly)
	}
	all := f.d.Links.All()
	if len(all) == 0 {
		return reply("No members are linked.")
	}
	var b strings.Builder
	if len(all) == 1 {
		b.WriteString("1 member is linked:")
	} else {
		fmt.Fprintf(&b, "%d members are linked:", len(all))
	}
	for i, l := range all {
		line := "\n<@" + l.DiscordID + "> → " + escapeMarkdown(l.UserName)
		if b.Len()+len(line) > listMax {
			fmt.Fprintf(&b, "\nand %d more", len(all)-i)
			break
		}
		b.WriteString(line)
	}
	return reply(b.String())
}

// handleAdminRemove does for another member what /unlink does for the caller.
func (f *Feature) handleAdminRemove(ctx context.Context, in *core.Interaction) error {
	if !in.IsAdmin {
		return f.d.DC.Respond(ctx, in, core.Message{Content: textAdminOnly}, true)
	}
	member := strings.TrimSpace(in.Options["member"])
	if !snowflake(member) {
		return f.d.DC.Respond(ctx, in, core.Message{Content: "Choose the member to unlink: `/link-admin remove member:<member>`."}, true)
	}
	if err := f.d.DC.Defer(ctx, in, true); err != nil {
		return err
	}
	l, res := f.unlink(ctx, member, in.UserID)
	who := "<@" + member + ">"
	var text string
	switch res {
	case unlinkDone:
		text = who + " is no longer linked to **" + escapeMarkdown(l.UserName) + "**, here and in HolyArrs."
	case unlinkHadPending:
		text = who + " was not linked yet. I stopped waiting for their link code. " +
			"If they still type it in HolyArrs before it expires, they will be linked."
	case unlinkNotLinked:
		text = who + " is not linked."
	case unlinkUnsupported:
		text = textUnsupported
	default:
		text = "I could not remove that link. Try again in a minute."
	}
	err := f.d.DC.FollowUp(ctx, in, core.Message{Content: text}, true)
	if res == unlinkDone {
		f.removeRole(ctx, member)
	}
	return err
}

func (f *Feature) handleWhoami(ctx context.Context, in *core.Interaction) error {
	reply := func(text string) error {
		return f.d.DC.Respond(ctx, in, core.Message{Content: text}, true)
	}
	if l, ok := f.d.Links.ByDiscord(in.UserID); ok {
		text := "You are linked to HolyArrs as **" + escapeMarkdown(l.UserName) + "**"
		if !l.LinkedAt.IsZero() {
			text += fmt.Sprintf(" since <t:%d:D>", l.LinkedAt.Unix())
		}
		text += ".\n"
		if l.ShowWatching {
			text += "Privacy: your name is shown next to what you watch."
		} else {
			text += "Privacy: your name is hidden from watch activity."
		}
		if l.AlertsOff {
			text += "\nAlerts: off. You get no messages about your requests."
		} else {
			text += "\nAlerts: on. You get a message when a request of yours is approved, declined or available."
		}
		return reply(text + "\nChange either with /privacy.")
	}
	if err := f.load(); err != nil {
		f.d.Log.Error("linking: /whoami", "err", err)
	}
	if exp, ok := f.pendingFor(in.UserID); ok && f.d.Now().Before(exp) {
		return reply("You are not linked yet. Your code expires " + relTime(exp) +
			": type it in HolyArrs under " + whereToEnter + ".")
	}
	return reply("You are not linked. Run /link, then type the code in HolyArrs under " + whereToEnter + ".")
}

// ---- helpers ----

func haDown(err error) string {
	if errors.Is(err, core.ErrRateLimited) {
		return "HolyArrs is busy right now. Try again in a minute."
	}
	if errors.Is(err, core.ErrForbidden) {
		return "HolyArrs did not let me do that. Ask the server owner to check the plugin's permissions."
	}
	return "I could not reach HolyArrs. Try again in a minute."
}

func relTime(t time.Time) string { return fmt.Sprintf("<t:%d:R>", t.Unix()) }

// escapeMarkdown makes HolyArrs-supplied text safe to show in a Discord
// message: no formatting, no mentions, no line breaks.
func escapeMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\', '*', '_', '~', '`', '|', '>', '<', '#', '[', ']', '(', ')', ':', '-':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '@':
			b.WriteString("@​")
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
