package linking

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/coretest"
)

// ---- rig ----

// Discord ids as HolyArrs hands them back in "external".
const (
	dA = "302845117690216449"
	dB = "302845117690216450"
	dC = "302845117690216451"

	testCode = "ABC-123"
	testRole = "role-linked"
)

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// logBuf collects everything the feature logs, at every level.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type rig struct {
	f    *Feature
	deps core.Deps
	ha   *coretest.FakeHolyArrs
	dc   *coretest.FakeDiscord
	st   *coretest.FakeStore
	lk   *coretest.FakeLinks
	clk  *clock
	log  *logBuf
}

func newRig(t *testing.T) *rig {
	t.Helper()
	deps, fk := coretest.NewDeps(t)
	r := &rig{ha: fk.HA, dc: fk.DC, st: fk.Store, lk: fk.Links, clk: &clock{t: t0}, log: &logBuf{}}
	deps.Cfg.LinkedRoleID = testRole
	deps.Cfg.InviteURL = "https://discord.gg/abc"
	deps.Cfg.EnableHomeWidget = false
	deps.Now = r.clk.now
	deps.Log = slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.ha.UsersVal = []core.User{{ID: "5", Name: "Alice"}, {ID: "7", Name: "Kim_*"}, {ID: "9", Name: "Bob"}}
	r.ha.LinkCode = testCode
	r.ha.LinkExpires = t0.Add(10 * time.Minute)
	r.deps = deps
	r.f = New(deps)
	return r
}

// restart builds a new Feature over the same fakes, as a new process would.
func (r *rig) restart() { r.f = New(r.deps) }

// run calls "/name" or "/parent sub" and returns the reply the caller saw.
func (r *rig) run(t *testing.T, command, discordID string, admin bool, opts map[string]string) string {
	t.Helper()
	in := &core.Interaction{UserID: discordID, Command: command, IsAdmin: admin, Options: opts}
	var handle func(context.Context, *core.Interaction) error
	for _, c := range r.f.Commands() {
		if c.Name == command {
			handle = c.Handle
		}
		for _, s := range c.Sub {
			if c.Name+" "+s.Name == command {
				handle = s.Handle
			}
		}
	}
	if handle == nil {
		t.Fatalf("no command %q", command)
	}
	if err := handle(context.Background(), in); err != nil {
		t.Fatalf("/%s: %v", command, err)
	}
	return r.last(t)
}

func (r *rig) cmd(t *testing.T, command, discordID string) string {
	t.Helper()
	return r.run(t, command, discordID, false, nil)
}

// last is the newest reply; every reply and deferral must be ephemeral.
func (r *rig) last(t *testing.T) string {
	t.Helper()
	text, found := "", false
	for _, c := range r.dc.Calls() {
		switch c.Method {
		case "Respond", "FollowUp":
			if !c.Args[2].(bool) {
				t.Fatalf("%s was not ephemeral", c.Method)
			}
			text, found = c.Args[1].(core.Message).Content, true
		case "Defer":
			if !c.Args[1].(bool) {
				t.Fatal("Defer was not ephemeral")
			}
		}
	}
	if !found {
		t.Fatal("no reply sent")
	}
	return text
}

type dm struct{ user, text string }

func (r *rig) dms() []dm {
	var out []dm
	for _, c := range r.dc.CallsTo("DM") {
		out = append(out, dm{c.Args[0].(string), c.Args[1].(core.Message).Content})
	}
	return out
}

// roles lists the role calls as "add:<user>" and "remove:<user>".
func (r *rig) roles(t *testing.T) string {
	t.Helper()
	var out []string
	for _, c := range r.dc.Calls() {
		op := map[string]string{"AddRole": "add", "RemoveRole": "remove"}[c.Method]
		if op == "" {
			continue
		}
		if c.Args[1].(string) != testRole {
			t.Errorf("%s with role %v", c.Method, c.Args[1])
		}
		out = append(out, op+":"+c.Args[0].(string))
	}
	return strings.Join(out, ",")
}

func (r *rig) remote(links ...core.RemoteLink) {
	r.ha.Set(func(h *coretest.FakeHolyArrs) { h.LinksVal = append([]core.RemoteLink(nil), links...) })
}

// removed lists the user ids RemoveLink was called for, in order.
func (r *rig) removed() string {
	var out []string
	for _, c := range r.ha.CallsTo("RemoveLink") {
		out = append(out, string(c.Args[0].(core.UserID)))
	}
	return strings.Join(out, ",")
}

// waiting lists the Discord ids with a pending entry, expired or not.
func (r *rig) waiting() string {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	var out []string
	for _, id := range []string{dA, dB, dC} {
		if _, ok := r.f.pending[id]; ok {
			out = append(out, id)
		}
	}
	return strings.Join(out, ",")
}

func (r *rig) reconcile() { r.f.reconcile(context.Background()) }

func (r *rig) put(t *testing.T, l core.Link) {
	t.Helper()
	if err := r.lk.Put(l); err != nil {
		t.Fatal(err)
	}
	r.lk.ResetCalls()
}

func (r *rig) linkOf(discordID string) string {
	l, ok := r.lk.ByDiscord(discordID)
	if !ok {
		return ""
	}
	return string(l.UserID)
}

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func wantContains(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("%q does not contain %q", got, s)
		}
	}
}

// ---- shape ----

func TestFeatureShape(t *testing.T) {
	r := newRig(t)
	var _ core.ComponentFeature = r.f
	var _ core.PageHandler = r.f.ServePage
	if r.f.Name() != "linking" {
		t.Errorf("Name = %q", r.f.Name())
	}
	var names []string
	for _, c := range r.f.Commands() {
		names = append(names, c.Name)
		switch c.Name {
		case "link", "unlink", "whoami":
			if len(c.Options) != 0 || c.AdminOnly {
				t.Errorf("/%s: options %v, admin %v", c.Name, c.Options, c.AdminOnly)
			}
		case "link-admin":
			if !c.AdminOnly || len(c.Sub) != 2 || c.Sub[0].Name != "list" || c.Sub[1].Name != "remove" {
				t.Fatalf("/link-admin: %+v", c)
			}
			if !c.Sub[0].AdminOnly || !c.Sub[1].AdminOnly || len(c.Sub[0].Options) != 0 ||
				len(c.Sub[1].Options) != 1 || c.Sub[1].Options[0].Name != "member" ||
				c.Sub[1].Options[0].Type != core.OptUser || !c.Sub[1].Options[0].Required {
				t.Errorf("/link-admin subs: %+v", c.Sub)
			}
		}
	}
	if got := strings.Join(names, ","); got != "link,unlink,whoami,link-admin" {
		t.Errorf("commands = %s", got)
	}
	if err := r.f.HandleComponent(context.Background(), &core.Interaction{CustomID: "linking:nope"}); err == nil {
		t.Error("HandleComponent accepted an unknown id")
	}
	if r.f.FastEvery != 5*time.Second || r.f.SlowEvery != 5*time.Minute {
		t.Errorf("cadence %v / %v", r.f.FastEvery, r.f.SlowEvery)
	}
}

// ---- /link and adoption ----

func TestLinkHappyPath(t *testing.T) {
	r := newRig(t)
	reply := r.cmd(t, "link", dA)
	wantContains(t, reply, "`"+testCode+"`", fmt.Sprintf("<t:%d:R>", at(10).Unix()), "My account → Linked plugins", "type this code")
	if calls := r.ha.CallsTo("NewLinkCode"); len(calls) != 1 || calls[0].Args[0] != dA {
		t.Fatalf("NewLinkCode calls = %v", calls)
	}
	if len(r.dc.CallsTo("Defer")) != 1 || len(r.dc.CallsTo("FollowUp")) != 1 {
		t.Errorf("want one Defer and one FollowUp, got %v", r.dc.Calls())
	}
	raw, _ := r.st.Raw(pendingDoc)
	if !strings.Contains(string(raw), dA) || strings.Contains(string(raw), testCode) {
		t.Fatalf("pending.json = %s", raw)
	}

	// Not typed yet: nothing happens.
	r.reconcile()
	if r.linkOf(dA) != "" || len(r.dms()) != 0 || r.waiting() != dA {
		t.Fatalf("linked before the code was typed: link %q dms %v", r.linkOf(dA), r.dms())
	}

	r.remote(core.RemoteLink{UserID: "7", External: dA, CreatedAt: at(1)})
	r.clk.add(time.Minute)
	r.reconcile()
	l, ok := r.lk.ByDiscord(dA)
	if !ok || l.UserID != "7" || l.UserName != "Kim_*" || l.ShowWatching || !l.LinkedAt.Equal(at(1)) {
		t.Fatalf("link = %+v, %v", l, ok)
	}
	if r.roles(t) != "add:"+dA {
		t.Errorf("roles = %s", r.roles(t))
	}
	if d := r.dms(); len(d) != 1 || d[0].user != dA || !strings.Contains(d[0].text, `**Kim\_\***`) {
		t.Errorf("dms = %v", d)
	}
	if r.waiting() != "" {
		t.Errorf("pending not dropped: %s", r.waiting())
	}
	if raw, _ := r.st.Raw(pendingDoc); strings.Contains(string(raw), dA) {
		t.Errorf("pending.json still holds the member: %s", raw)
	}

	// Later passes change nothing and say nothing.
	r.reconcile()
	r.reconcile()
	if len(r.dms()) != 1 || len(r.lk.CallsTo("Put")) != 1 || r.removed() != "" || r.roles(t) != "add:"+dA {
		t.Errorf("a settled link was touched again: dms %v, puts %d, removed %q", r.dms(), len(r.lk.CallsTo("Put")), r.removed())
	}

	// The code is shown to the member and goes nowhere else.
	if strings.Contains(r.log.String(), testCode) {
		t.Errorf("the link code was logged:\n%s", r.log.String())
	}
	for _, d := range r.dms() {
		if strings.Contains(d.text, testCode) {
			t.Errorf("the link code was sent in a DM: %q", d.text)
		}
	}
}

func TestSeveralMembersPendingAtOnce(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{dA, dB, dC} {
		wantContains(t, r.cmd(t, "link", id), "`"+testCode+"`")
	}
	if r.waiting() != dA+","+dB+","+dC || len(r.ha.CallsTo("NewLinkCode")) != 3 {
		t.Fatalf("waiting %q, codes %d", r.waiting(), len(r.ha.CallsTo("NewLinkCode")))
	}
	if r.f.cadence() != r.f.FastEvery {
		t.Error("slow cadence while codes are waiting")
	}

	r.remote(core.RemoteLink{UserID: "5", External: dC, CreatedAt: at(1)}, core.RemoteLink{UserID: "9", External: dA, CreatedAt: at(2)})
	r.reconcile()
	if r.linkOf(dA) != "9" || r.linkOf(dC) != "5" || r.linkOf(dB) != "" {
		t.Fatalf("links: A=%q B=%q C=%q", r.linkOf(dA), r.linkOf(dB), r.linkOf(dC))
	}
	if r.waiting() != dB || len(r.dms()) != 2 || r.roles(t) != "add:"+dA+",add:"+dC {
		t.Errorf("waiting %q, dms %v, roles %s", r.waiting(), r.dms(), r.roles(t))
	}
	if n := len(r.ha.CallsTo("Users")); n != 1 {
		t.Errorf("Users read %d times in one pass, want 1", n)
	}

	// B never types the code: the entry goes when it expires, quietly.
	r.clk.add(10 * time.Minute)
	r.reconcile()
	if r.waiting() != "" || len(r.dms()) != 2 || r.f.cadence() != r.f.SlowEvery {
		t.Errorf("after expiry: waiting %q, dms %v", r.waiting(), r.dms())
	}
	if raw, _ := r.st.Raw(pendingDoc); strings.Contains(string(raw), dB) {
		t.Errorf("pending.json = %s", raw)
	}
}

func TestLinkAgainWhilePendingGivesFreshCode(t *testing.T) {
	r := newRig(t)
	r.cmd(t, "link", dA)
	r.clk.add(3 * time.Minute)
	r.ha.Set(func(h *coretest.FakeHolyArrs) { h.LinkCode, h.LinkExpires = "NEW-999", at(13) })
	wantContains(t, r.cmd(t, "link", dA), "`NEW-999`", fmt.Sprintf("<t:%d:R>", at(13).Unix()))
	if exp, ok := r.f.pendingFor(dA); !ok || !exp.Equal(at(13)) || r.waiting() != dA {
		t.Errorf("pending = %v %v", exp, ok)
	}
	if len(r.ha.CallsTo("NewLinkCode")) != 2 {
		t.Errorf("codes asked: %d", len(r.ha.CallsTo("NewLinkCode")))
	}
}

func TestLinkRefusedWhenLinked(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	wantContains(t, r.cmd(t, "link", dA), `**Kim\_\***`, "/unlink first")
	if len(r.ha.Calls()) != 0 || r.waiting() != "" {
		t.Errorf("HolyArrs was called or a code is waiting: %v, %q", r.ha.Calls(), r.waiting())
	}
}

func TestLinkWhenHolyArrsFails(t *testing.T) {
	for _, tc := range []struct {
		method string
		err    error
		want   string
	}{
		{"Links", errors.New("down"), "could not reach HolyArrs"},
		{"Links", core.ErrForbidden, "permissions"},
		{"NewLinkCode", core.ErrRateLimited, "busy"},
		{"NewLinkCode", errors.New("boom"), "Something went wrong"},
	} {
		r := newRig(t)
		r.ha.SetErr(tc.method, tc.err)
		reply := r.cmd(t, "link", dA)
		wantContains(t, reply, tc.want)
		if strings.Contains(reply, testCode) || r.waiting() != "" {
			t.Errorf("%s %v: a code was handed out: %q", tc.method, tc.err, reply)
		}
	}
	// An empty code is never shown.
	r := newRig(t)
	r.ha.LinkCode = " ` "
	wantContains(t, r.cmd(t, "link", dA), "Something went wrong")
}

func TestLinkAsksAboutSupportOnlyUntilKnown(t *testing.T) {
	r := newRig(t)
	r.cmd(t, "link", dA)
	r.cmd(t, "link", dB)
	if n := len(r.ha.CallsTo("Links")); n != 1 {
		t.Errorf("Links read %d times by /link, want 1", n)
	}
	// A code with no expiry from HolyArrs gets the default life.
	r.ha.Set(func(h *coretest.FakeHolyArrs) { h.LinkExpires = time.Time{} })
	wantContains(t, r.cmd(t, "link", dC), fmt.Sprintf("<t:%d:R>", at(10).Unix()))
}

func TestAdoptWithoutPendingEntry(t *testing.T) {
	// The pending book was lost (or the code typed after a restart): the
	// external id still proves which member asked for the code.
	r := newRig(t)
	r.remote(core.RemoteLink{UserID: "9", External: dB, CreatedAt: at(0)})
	r.reconcile()
	if r.linkOf(dB) != "9" || len(r.dms()) != 1 || r.roles(t) != "add:"+dB {
		t.Fatalf("link %q, dms %v, roles %s", r.linkOf(dB), r.dms(), r.roles(t))
	}
	// The DM says what linking turns on and how to turn it off.
	wantContains(t, r.dms()[0].text, "You are now linked", "each time one of your requests is available", "/privacy alerts:off")
	if l, _ := r.lk.ByDiscord(dB); l.AlertsOff || l.ShowWatching {
		t.Errorf("new link = %+v, want alerts on and the name hidden", l)
	}
}

func TestNonSnowflakeExternalIgnored(t *testing.T) {
	r := newRig(t)
	r.remote(
		core.RemoteLink{UserID: "5", External: "discord-42"},
		core.RemoteLink{UserID: "7", External: "12345678901234"},         // 14 digits
		core.RemoteLink{UserID: "9", External: "1234567890123456789012"}, // 22 digits
		core.RemoteLink{UserID: "11", External: "30284511769021644x"},
		core.RemoteLink{UserID: "12", External: ""},
		core.RemoteLink{UserID: "13", External: "<@302845117690216449>"},
	)
	r.reconcile()
	if len(r.lk.All()) != 0 || len(r.dc.Calls()) != 0 || r.removed() != "" || len(r.ha.CallsTo("Users")) != 0 {
		t.Fatalf("links %v, discord %v, removed %q", r.lk.All(), r.dc.Calls(), r.removed())
	}
	for s, want := range map[string]bool{"123456789012345": true, "123456789012345678901": true, dA: true, "": false, "１２３４５６７８９０１２３４５": false} {
		if snowflake(s) != want {
			t.Errorf("snowflake(%q) = %v", s, !want)
		}
	}
}

func TestNameLookup(t *testing.T) {
	// Not in the user list: the id stands in for the name.
	r := newRig(t)
	r.remote(core.RemoteLink{UserID: "42", External: dA})
	r.reconcile()
	if l, _ := r.lk.ByDiscord(dA); l.UserName != "42" {
		t.Errorf("name = %q, want the id", l.UserName)
	}

	// The list cannot be read: wait for the next pass instead of guessing.
	r = newRig(t)
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	r.ha.SetErr("Users", errors.New("down"))
	r.reconcile()
	if r.linkOf(dA) != "" || len(r.dms()) != 0 {
		t.Fatalf("adopted without a name: %q", r.linkOf(dA))
	}
	r.ha.SetErr("Users", nil)
	r.reconcile()
	if l, _ := r.lk.ByDiscord(dA); l.UserName != "Kim_*" {
		t.Errorf("name = %q after the retry", l.UserName)
	}
}

func TestAdoptSurvivesDiscordFailures(t *testing.T) {
	r := newRig(t)
	r.dc.SetErr("DM", errors.New("discord down"))
	r.dc.SetErr("AddRole", errors.New("missing permission"))
	r.remote(core.RemoteLink{UserID: "7", External: dA}, core.RemoteLink{UserID: "9", External: dB})
	r.reconcile()
	if r.linkOf(dA) != "7" || r.linkOf(dB) != "9" {
		t.Fatalf("links: A=%q B=%q", r.linkOf(dA), r.linkOf(dB))
	}
	wantContains(t, r.log.String(), "linking: dm", "linking: add role")

	// Closed DMs are normal and not worth a warning.
	r = newRig(t)
	r.dc.SetErr("DM", core.ErrForbidden)
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	r.reconcile()
	if r.linkOf(dA) != "7" || strings.Contains(r.log.String(), "linking: dm") {
		t.Errorf("link %q, log %s", r.linkOf(dA), r.log.String())
	}
}

func TestAdoptWhenTheLinkCannotBeSaved(t *testing.T) {
	r := newRig(t)
	r.cmd(t, "link", dA)
	r.lk.SetErr("Put", errors.New("disk full"))
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	r.reconcile()
	if r.linkOf(dA) != "" || len(r.dms()) != 0 || r.roles(t) != "" || r.waiting() != dA {
		t.Fatalf("link %q, dms %v, roles %q, waiting %q", r.linkOf(dA), r.dms(), r.roles(t), r.waiting())
	}
	r.lk.SetErr("Put", nil)
	r.reconcile()
	if r.linkOf(dA) != "7" || len(r.dms()) != 1 {
		t.Errorf("not linked on the next pass: %q", r.linkOf(dA))
	}
}

func TestNoRoleConfigured(t *testing.T) {
	r := newRig(t)
	r.deps.Cfg.LinkedRoleID = ""
	r.restart()
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	r.reconcile()
	wantContains(t, r.cmd(t, "unlink", dA), "no longer linked")
	r.put(t, core.Link{DiscordID: dB, UserID: "9", UserName: "Bob"})
	r.reconcile() // B is gone in HolyArrs
	if r.linkOf(dB) != "" || r.roles(t) != "" {
		t.Errorf("link %q, roles %q; want none", r.linkOf(dB), r.roles(t))
	}
}

// ---- duplicates ----

func TestDuplicateExternal(t *testing.T) {
	t.Run("oldest wins, lowest id on a tie", func(t *testing.T) {
		r := newRig(t)
		r.remote(
			core.RemoteLink{UserID: "7", External: dA, CreatedAt: at(5)},
			core.RemoteLink{UserID: "9", External: dA, CreatedAt: at(1)},
			core.RemoteLink{UserID: "5", External: dA, CreatedAt: at(1)},
			core.RemoteLink{UserID: "10", External: dB, CreatedAt: at(2)},
			core.RemoteLink{UserID: "9000", External: dB, CreatedAt: at(2)},
		)
		r.reconcile()
		if r.linkOf(dA) != "5" || r.linkOf(dB) != "10" {
			t.Fatalf("links: A=%q B=%q", r.linkOf(dA), r.linkOf(dB))
		}
		if r.removed() != "7,9,9000" {
			t.Errorf("removed in HolyArrs: %q", r.removed())
		}
		if len(r.dms()) != 2 || r.roles(t) != "add:"+dA+",add:"+dB {
			t.Errorf("dms %v, roles %s", r.dms(), r.roles(t))
		}
		// HolyArrs now holds one link each: the next pass is quiet.
		r.reconcile()
		if r.removed() != "7,9,9000" || len(r.dms()) != 2 || len(r.lk.All()) != 2 {
			t.Errorf("second pass: removed %q, dms %v", r.removed(), r.dms())
		}
	})

	t.Run("the local link wins over an older one", func(t *testing.T) {
		r := newRig(t)
		r.put(t, core.Link{DiscordID: dA, UserID: "9", UserName: "Bob", ShowWatching: true, LinkedAt: at(-60)})
		r.remote(
			core.RemoteLink{UserID: "5", External: dA, CreatedAt: at(1)},
			core.RemoteLink{UserID: "9", External: dA, CreatedAt: at(5)},
		)
		r.reconcile()
		l, _ := r.lk.ByDiscord(dA)
		if l.UserID != "9" || !l.ShowWatching || !l.LinkedAt.Equal(at(-60)) {
			t.Fatalf("link = %+v", l)
		}
		if r.removed() != "5" || len(r.dc.Calls()) != 0 || len(r.lk.Calls()) != 0 {
			t.Errorf("removed %q, discord %v, link writes %v", r.removed(), r.dc.Calls(), r.lk.Calls())
		}
	})

	t.Run("linked member types a code from another account", func(t *testing.T) {
		r := newRig(t)
		r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
		r.remote(
			core.RemoteLink{UserID: "7", External: dA, CreatedAt: at(-60)},
			core.RemoteLink{UserID: "9", External: dA, CreatedAt: at(1)},
		)
		r.reconcile()
		if r.linkOf(dA) != "7" || r.removed() != "9" || len(r.dms()) != 0 {
			t.Errorf("link %q, removed %q, dms %v", r.linkOf(dA), r.removed(), r.dms())
		}
	})

	t.Run("a duplicate that cannot be removed is never adopted", func(t *testing.T) {
		r := newRig(t)
		r.ha.SetErr("RemoveLink", errors.New("down"))
		r.remote(
			core.RemoteLink{UserID: "7", External: dA, CreatedAt: at(1)},
			core.RemoteLink{UserID: "9", External: dA, CreatedAt: at(2)},
		)
		r.reconcile()
		r.reconcile()
		if r.linkOf(dA) != "7" || len(r.lk.All()) != 1 || len(r.dms()) != 1 {
			t.Errorf("link %q, all %v, dms %v", r.linkOf(dA), r.lk.All(), r.dms())
		}
		if r.removed() != "9,9" {
			t.Errorf("removal tried for %q, want it retried each pass", r.removed())
		}
	})
}

// ---- HolyArrs is the source of truth ----

func TestLocalLinkRemovedWhenRemoteIsGone(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.put(t, core.Link{DiscordID: dB, UserID: "9", UserName: "Bob"})
	r.remote(core.RemoteLink{UserID: "7", External: dA}, core.RemoteLink{UserID: "9", External: dB})
	r.reconcile()
	if len(r.lk.All()) != 2 || len(r.dc.Calls()) != 0 {
		t.Fatalf("matching links were touched: %v, %v", r.lk.All(), r.dc.Calls())
	}

	r.remote(core.RemoteLink{UserID: "9", External: dB})
	r.reconcile()
	if r.linkOf(dA) != "" || r.linkOf(dB) != "9" {
		t.Fatalf("links: A=%q B=%q", r.linkOf(dA), r.linkOf(dB))
	}
	if r.roles(t) != "remove:"+dA || len(r.dms()) != 0 || r.removed() != "" {
		t.Errorf("roles %s, dms %v, removed %q", r.roles(t), r.dms(), r.removed())
	}

	// The last link goes on an empty list too: that is a real answer.
	r.remote()
	r.reconcile()
	if len(r.lk.All()) != 0 || r.roles(t) != "remove:"+dA+",remove:"+dB {
		t.Errorf("links %v, roles %s", r.lk.All(), r.roles(t))
	}
}

func TestLocalLinkFollowsAChangedUser(t *testing.T) {
	// A is linked to 7 here, but HolyArrs now ties A to 9 only (7 removed
	// the plugin, 9 typed a code issued to A before the bot noticed).
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*", ShowWatching: true})
	r.remote(core.RemoteLink{UserID: "9", External: dA})
	r.reconcile()
	l, _ := r.lk.ByDiscord(dA)
	if l.UserID != "9" || l.UserName != "Bob" || l.ShowWatching {
		t.Fatalf("link = %+v", l)
	}
	if r.roles(t) != "remove:"+dA+",add:"+dA || r.removed() != "" {
		t.Errorf("roles %s, removed %q", r.roles(t), r.removed())
	}

	// User 7 moves from A to B in HolyArrs: the bot follows.
	r = newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.remote(core.RemoteLink{UserID: "7", External: dB})
	r.reconcile()
	if r.linkOf(dA) != "" || r.linkOf(dB) != "7" {
		t.Errorf("links: A=%q B=%q", r.linkOf(dA), r.linkOf(dB))
	}
}

func TestNoRemovalWhenLinksFails(t *testing.T) {
	for _, err := range []error{errors.New("connection refused"), core.ErrForbidden, core.ErrRateLimited, core.ErrUnsupported, context.DeadlineExceeded} {
		r := newRig(t)
		r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
		r.put(t, core.Link{DiscordID: dB, UserID: "9", UserName: "Bob"})
		r.remote() // what a careless reading of a failure would look like
		r.ha.SetErr("Links", err)
		for range 3 {
			r.reconcile()
		}
		if len(r.lk.All()) != 2 || len(r.lk.Calls()) != 0 || len(r.dc.Calls()) != 0 || r.removed() != "" {
			t.Errorf("%v: links %v, link writes %v, discord %v", err, r.lk.All(), r.lk.Calls(), r.dc.Calls())
		}
	}
}

func TestStaleLinkStaysWhenItCannotBeRemovedHere(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.lk.SetErr("Remove", errors.New("disk full"))
	r.remote(core.RemoteLink{UserID: "9", External: dA})
	r.reconcile()
	if r.linkOf(dA) != "7" || r.roles(t) != "" || len(r.dms()) != 0 {
		t.Errorf("link %q, roles %q, dms %v", r.linkOf(dA), r.roles(t), r.dms())
	}
}

// ---- an older HolyArrs ----

func TestUnsupported(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.ha.SetErr("Links", fmt.Errorf("holyarrs: status 404: %w", core.ErrUnsupported))
	r.ha.SetErr("RemoveLink", fmt.Errorf("holyarrs: status 405: %w", core.ErrUnsupported))

	if got := r.cmd(t, "link", dB); got != textUnsupported {
		t.Errorf("/link = %q", got)
	}
	if len(r.ha.CallsTo("NewLinkCode")) != 0 || r.waiting() != "" {
		t.Error("a code was asked for on a HolyArrs that cannot report links")
	}
	if got := r.cmd(t, "unlink", dA); got != textUnsupported {
		t.Errorf("/unlink = %q", got)
	}
	if got := r.run(t, "link-admin remove", dC, true, map[string]string{"member": dA}); got != textUnsupported {
		t.Errorf("/link-admin remove = %q", got)
	}
	if r.linkOf(dA) != "7" || r.roles(t) != "" {
		t.Errorf("link %q, roles %q: nothing may change", r.linkOf(dA), r.roles(t))
	}
	if textUnsupported != "Linking needs a newer HolyArrs. Ask the server owner to update." {
		t.Errorf("text = %q", textUnsupported)
	}

	// The loop says so once and stays on the slow cadence, pending or not.
	r.f.setPending(dB, at(10))
	for range 4 {
		r.reconcile()
	}
	if n := strings.Count(r.log.String(), "cannot list links"); n != 1 {
		t.Errorf("warned %d times, want 1:\n%s", n, r.log.String())
	}
	if !strings.Contains(r.log.String(), "level=WARN") || r.f.cadence() != r.f.SlowEvery {
		t.Errorf("cadence %v, log %s", r.f.cadence(), r.log.String())
	}

	// After an update everything works without a restart.
	r.ha.SetErr("Links", nil)
	r.ha.SetErr("RemoveLink", nil)
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	wantContains(t, r.cmd(t, "link", dB), "`"+testCode+"`")
	r.reconcile()
	if r.f.cadence() != r.f.FastEvery || r.linkOf(dA) != "7" {
		t.Errorf("cadence %v, link %q", r.f.cadence(), r.linkOf(dA))
	}
}

// ---- /unlink ----

func TestUnlink(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.put(t, core.Link{DiscordID: dB, UserID: "9", UserName: "Bob"})
	r.remote(core.RemoteLink{UserID: "7", External: dA}, core.RemoteLink{UserID: "9", External: dB})
	r.f.setPending(dA, at(10)) // a stray entry goes too

	wantContains(t, r.cmd(t, "unlink", dA), `**Kim\_\***`, "no longer linked", "in HolyArrs")
	if r.removed() != "7" || r.linkOf(dA) != "" || r.linkOf(dB) != "9" || r.waiting() != "" {
		t.Fatalf("removed %q, A=%q, B=%q, waiting %q", r.removed(), r.linkOf(dA), r.linkOf(dB), r.waiting())
	}
	// Deferred, answered, and only then the slow role call.
	var order []string
	for _, c := range r.dc.Calls() {
		order = append(order, c.Method)
	}
	if got := strings.Join(order, ","); got != "Defer,FollowUp,RemoveRole" {
		t.Errorf("order = %s", got)
	}
	if r.roles(t) != "remove:"+dA {
		t.Errorf("roles = %s", r.roles(t))
	}
	// HolyArrs agrees, so the next pass leaves B alone and does not bring A back.
	r.reconcile()
	if r.linkOf(dA) != "" || r.linkOf(dB) != "9" || len(r.dms()) != 0 {
		t.Errorf("after the pass: A=%q B=%q dms %v", r.linkOf(dA), r.linkOf(dB), r.dms())
	}
}

func TestUnlinkFailureKeepsTheLink(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	r.ha.SetErr("RemoveLink", errors.New("down"))
	wantContains(t, r.cmd(t, "unlink", dA), "could not remove your link", "Try /unlink again")
	if r.linkOf(dA) != "7" || r.roles(t) != "" || len(r.lk.Calls()) != 0 {
		t.Fatalf("link %q, roles %q, link writes %v", r.linkOf(dA), r.roles(t), r.lk.Calls())
	}
	r.reconcile()
	if r.linkOf(dA) != "7" {
		t.Fatal("the pass removed a link HolyArrs still holds")
	}

	// Removed in HolyArrs but not saved here: said plainly, and the next
	// pass finishes the job.
	r.ha.SetErr("RemoveLink", nil)
	r.lk.SetErr("Remove", errors.New("disk full"))
	wantContains(t, r.cmd(t, "unlink", dA), "could not remove your link")
	if r.linkOf(dA) != "7" || r.roles(t) != "" {
		t.Fatalf("link %q, roles %q", r.linkOf(dA), r.roles(t))
	}
	r.lk.SetErr("Remove", nil)
	r.reconcile()
	if r.linkOf(dA) != "" || r.roles(t) != "remove:"+dA {
		t.Errorf("link %q, roles %q", r.linkOf(dA), r.roles(t))
	}
}

func TestUnlinkWhenNotLinked(t *testing.T) {
	r := newRig(t)
	wantContains(t, r.cmd(t, "unlink", dA), "You are not linked", "/link")
	r.cmd(t, "link", dA)
	wantContains(t, r.cmd(t, "unlink", dA), "not linked yet", "stopped waiting")
	if r.waiting() != "" || r.removed() != "" || r.roles(t) != "" {
		t.Errorf("waiting %q, removed %q, roles %q", r.waiting(), r.removed(), r.roles(t))
	}
	// An expired entry is not worth mentioning.
	r.f.setPending(dA, at(-1))
	wantContains(t, r.cmd(t, "unlink", dA), "You are not linked")
}

// ---- /whoami ----

func TestWhoami(t *testing.T) {
	tests := []struct {
		name string
		link *core.Link
		want []string
	}{
		{name: "not linked", want: []string{"not linked", "Run /link", "My account → Linked plugins"}},
		{
			name: "linked, hidden",
			link: &core.Link{DiscordID: dA, UserID: "7", UserName: "Al*ce", LinkedAt: t0},
			want: []string{`**Al\*ce**`, "<t:1795608000:D>", "hidden"},
		},
		{
			name: "linked, shown",
			link: &core.Link{DiscordID: dA, UserID: "7", UserName: "Alice", ShowWatching: true},
			want: []string{"**Alice**", "is shown", "Alerts: on"},
		},
		{
			name: "linked, alerts off",
			link: &core.Link{DiscordID: dA, UserID: "7", UserName: "Alice", AlertsOff: true},
			want: []string{"hidden", "Alerts: off", "/privacy"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			if tc.link != nil {
				r.put(t, *tc.link)
			}
			got := r.cmd(t, "whoami", dA)
			wantContains(t, got, tc.want...)
			if strings.Contains(got, "name:") {
				t.Errorf("reply still asks for a name: %q", got)
			}
		})
	}
	r := newRig(t)
	r.cmd(t, "link", dA)
	got := r.cmd(t, "whoami", dA)
	wantContains(t, got, "not linked yet", fmt.Sprintf("<t:%d:R>", at(10).Unix()))
	if strings.Contains(got, testCode) {
		t.Errorf("/whoami repeated the code: %q", got)
	}
}

// ---- /link-admin ----

func TestLinkAdminGating(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	for _, sub := range []string{"link-admin list", "link-admin remove"} {
		got := r.run(t, sub, dB, false, map[string]string{"member": dA})
		if got != textAdminOnly {
			t.Errorf("/%s as a member = %q", sub, got)
		}
	}
	if r.linkOf(dA) != "7" || len(r.ha.Calls()) != 0 || r.roles(t) != "" || len(r.dc.CallsTo("Defer")) != 0 {
		t.Errorf("a member changed something: link %q, HolyArrs %v", r.linkOf(dA), r.ha.Calls())
	}
}

func TestLinkAdminList(t *testing.T) {
	r := newRig(t)
	if got := r.run(t, "link-admin list", dC, true, nil); got != "No members are linked." {
		t.Errorf("empty = %q", got)
	}
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	if got := r.run(t, "link-admin list", dC, true, nil); got != "1 member is linked:\n<@"+dA+"> → Kim\\_\\*" {
		t.Errorf("one = %q", got)
	}
	r.put(t, core.Link{DiscordID: dB, UserID: "9", UserName: "Bob"})
	if got := r.run(t, "link-admin list", dC, true, nil); got != "2 members are linked:\n<@"+dA+"> → Kim\\_\\*\n<@"+dB+"> → Bob" {
		t.Errorf("two = %q", got)
	}
	if len(r.ha.Calls()) != 0 {
		t.Errorf("the list called HolyArrs: %v", r.ha.Calls())
	}

	// More than fits in one message.
	const total = 302
	for i := range total - 2 {
		r.put(t, core.Link{DiscordID: fmt.Sprintf("4000000000000000%03d", i), UserID: core.UserID(fmt.Sprint(100 + i)), UserName: strings.Repeat("n", 20)})
	}
	got := r.run(t, "link-admin list", dC, true, nil)
	lines := strings.Split(got, "\n")
	shown := len(lines) - 2
	if len([]rune(got)) > 2000 || shown < 10 || lines[0] != "302 members are linked:" ||
		lines[len(lines)-1] != fmt.Sprintf("and %d more", total-shown) {
		t.Errorf("long list: %d characters, %d shown, first %q, last %q", len([]rune(got)), shown, lines[0], lines[len(lines)-1])
	}
}

func TestLinkAdminRemove(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.put(t, core.Link{DiscordID: dB, UserID: "9", UserName: "Bob"})
	r.remote(core.RemoteLink{UserID: "7", External: dA}, core.RemoteLink{UserID: "9", External: dB})

	got := r.run(t, "link-admin remove", dC, true, map[string]string{"member": dA})
	wantContains(t, got, "<@"+dA+">", `**Kim\_\***`, "no longer linked")
	if r.removed() != "7" || r.linkOf(dA) != "" || r.linkOf(dB) != "9" || r.roles(t) != "remove:"+dA {
		t.Fatalf("removed %q, A=%q, B=%q, roles %s", r.removed(), r.linkOf(dA), r.linkOf(dB), r.roles(t))
	}
	wantContains(t, r.log.String(), "by="+dC)

	wantContains(t, r.run(t, "link-admin remove", dC, true, map[string]string{"member": dA}), "<@"+dA+"> is not linked")
	for _, bad := range []string{"", "everyone", "@" + dB} {
		wantContains(t, r.run(t, "link-admin remove", dC, true, map[string]string{"member": bad}), "Choose the member")
	}
	if r.removed() != "7" || r.linkOf(dB) != "9" {
		t.Errorf("removed %q, B=%q", r.removed(), r.linkOf(dB))
	}

	r.ha.SetErr("RemoveLink", errors.New("down"))
	wantContains(t, r.run(t, "link-admin remove", dC, true, map[string]string{"member": dB}), "could not remove that link")
	if r.linkOf(dB) != "9" || r.roles(t) != "remove:"+dA {
		t.Errorf("B=%q, roles %s", r.linkOf(dB), r.roles(t))
	}
}

// ---- Run ----

// start runs the feature with the given waits and returns a stop function.
func start(t *testing.T, r *rig, fast, slow time.Duration) (stop func()) {
	t.Helper()
	r.f.FastEvery, r.f.SlowEvery = fast, slow
	r.f.PageRetry, r.f.WidgetRetry = time.Millisecond, time.Millisecond
	f := r.f
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestRunCadence(t *testing.T) {
	r := newRig(t)
	stop := start(t, r, time.Millisecond, time.Hour)
	links := func() int { return len(r.ha.CallsTo("Links")) }

	// Once at start, then the slow wait: nobody is pending.
	coretest.Eventually(t, "the first pass", func() bool { return links() == 1 })
	time.Sleep(20 * time.Millisecond)
	if links() != 1 {
		t.Fatalf("%d passes with nobody pending and an hour between them", links())
	}

	// A code wakes the loop into the fast cadence.
	r.cmd(t, "link", dA)
	coretest.Eventually(t, "fast passes while a code waits", func() bool { return links() >= 5 })
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	coretest.Eventually(t, "the link", func() bool { return r.linkOf(dA) == "7" })
	coretest.Eventually(t, "the DM", func() bool { return len(r.dms()) == 1 })

	// Linked: back to the slow wait.
	time.Sleep(5 * time.Millisecond)
	n := links()
	time.Sleep(20 * time.Millisecond)
	if links() > n+1 {
		t.Errorf("still polling fast after the link: %d -> %d", n, links())
	}

	stop()
	n = links()
	time.Sleep(10 * time.Millisecond)
	if links() != n {
		t.Error("the loop kept going after Run returned")
	}
}

func TestRunStopsFastCadenceWhenCodesExpire(t *testing.T) {
	r := newRig(t)
	r.cmd(t, "link", dA)
	start(t, r, time.Millisecond, time.Hour)
	links := func() int { return len(r.ha.CallsTo("Links")) }
	coretest.Eventually(t, "fast passes", func() bool { return links() >= 5 })
	r.clk.add(11 * time.Minute)
	time.Sleep(5 * time.Millisecond)
	n := links()
	time.Sleep(20 * time.Millisecond)
	if links() > n+1 {
		t.Errorf("still polling fast with no code waiting: %d -> %d", n, links())
	}
	// The expired entry itself goes on the next pass.
	r.reconcile()
	if r.waiting() != "" {
		t.Errorf("the expired entry stayed: %q", r.waiting())
	}
}

func TestRestartWithPending(t *testing.T) {
	r := newRig(t)
	r.cmd(t, "link", dA)
	r.cmd(t, "link", dB)
	r.restart() // pending.json is all that is left
	if r.waiting() != "" {
		t.Fatal("a new feature starts with nothing in memory")
	}
	r.ha.ResetCalls()
	start(t, r, time.Millisecond, time.Hour)
	coretest.Eventually(t, "the pending book to load", func() bool { return r.waiting() == dA+","+dB })
	coretest.Eventually(t, "fast passes after a restart", func() bool { return len(r.ha.CallsTo("Links")) >= 5 })
	r.remote(core.RemoteLink{UserID: "7", External: dA})
	coretest.Eventually(t, "the link", func() bool { return r.linkOf(dA) == "7" && r.waiting() == dB })
}

func TestRunSurvivesFailuresAndServesThePage(t *testing.T) {
	r := newRig(t)
	r.put(t, core.Link{DiscordID: dA, UserID: "7", UserName: "Kim_*"})
	r.ha.SetErr("Links", errors.New("down"))
	r.ha.SetErr("ServePages", errors.New("page server down"))
	stop := start(t, r, time.Millisecond, time.Millisecond)
	coretest.Eventually(t, "retries", func() bool {
		return len(r.ha.CallsTo("Links")) >= 3 && len(r.ha.CallsTo("ServePages")) >= 3
	})
	r.ha.SetErr("ServePages", nil)
	coretest.Eventually(t, "the page", func() bool { _, _, ok := r.ha.FetchPage("index.html"); return ok })
	if r.linkOf(dA) != "7" {
		t.Error("a link was removed while HolyArrs was down")
	}
	stop()

	// An unreadable pending book stops Run with an error for the supervisor.
	r = newRig(t)
	r.st.SetErr("Load", errors.New("bad disk"))
	if err := r.f.Run(context.Background()); err == nil {
		t.Error("Run with an unreadable pending book returned nil")
	}
}

func TestOldPendingFileIsRead(t *testing.T) {
	// The file an earlier version wrote: extra fields, tombstones, a blank id.
	r := newRig(t)
	r.st.SetRaw(pendingDoc, []byte(`{"pending":[
		{"discord_id":"`+dA+`","user_id":"7","user_name":"Kim","expires":"2026-11-25T12:05:00Z"},
		{"discord_id":"`+dB+`","user_id":"9","user_name":"Bob","expires":"2026-11-25T11:00:00Z","cancelled":true},
		{"discord_id":"","expires":"2026-11-25T12:05:00Z"}]}`))
	if err := r.f.load(); err != nil {
		t.Fatal(err)
	}
	if r.waiting() != dA+","+dB || r.f.cadence() != r.f.FastEvery {
		t.Fatalf("waiting %q", r.waiting())
	}
	r.reconcile()
	if r.waiting() != dA {
		t.Errorf("the expired entry stayed: %q", r.waiting())
	}
}

// ---- page ----

func TestPage(t *testing.T) {
	const unset = "Set the <strong>Invite link</strong> setting"
	tests := []struct {
		name    string
		invite  string
		want    []string
		wantNot []string
	}{
		{
			name:    "with invite",
			invite:  " https://discord.gg/abc ",
			want:    []string{`<pre class="copy">https://discord.gg/abc</pre>`},
			wantNot: []string{unset},
		},
		{
			name:    "without invite",
			want:    []string{unset},
			wantNot: []string{"<pre"},
		},
		{
			name:    "invite is escaped",
			invite:  `https://discord.gg/a"><script>alert(1)</script>&b=1`,
			want:    []string{`<pre class="copy">https://discord.gg/a&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;&amp;b=1</pre>`},
			wantNot: []string{`a">`},
		},
		{
			name:    "script link is not shown",
			invite:  "javascript:alert(1)",
			want:    []string{unset},
			wantNot: []string{"javascript:", "<pre"},
		},
		{
			name:    "no host is not shown",
			invite:  "discord.gg/abc",
			want:    []string{unset},
			wantNot: []string{"<pre"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := New(core.Deps{Cfg: core.Config{InviteURL: tc.invite}})
			pages := f.Pages()
			if len(pages) != 1 || pages[0] != (core.Page{ID: "discord", Title: "Discord", File: "index.html"}) {
				t.Fatalf("pages = %+v", pages)
			}
			body, ct, ok := f.ServePage("index.html")
			if !ok || !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("ServePage ok=%v contentType=%q", ok, ct)
			}
			html := string(body)
			wantContains(t, html, tc.want...)
			wantContains(t, html, "<!doctype html>", "prefers-color-scheme: dark", "Share this with your members",
				"<ol>", "Join the Discord server", "Run <code>/link</code> in Discord", "type the code under",
				"My account &rarr; Linked plugins", "/privacy", "opt in")
			// The page sandbox blocks links and popups: nothing clickable, nothing loaded.
			for _, s := range append(tc.wantNot, "<a ", "<a>", "href", "<button", "<form", "<input", "onclick",
				"target=", "<script", "<link", "<img", "<iframe", "@import", "url(", "HolyArrs name") {
				if strings.Contains(html, s) {
					t.Errorf("page contains %q", s)
				}
			}
			if _, _, ok := f.ServePage("other.html"); ok {
				t.Error("served an unknown file")
			}
		})
	}
}

// ---- helpers ----

func TestEscapeMarkdown(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Alice", "Alice"},
		{"a_b*c", `a\_b\*c`},
		{"`x` ~y~ |z|", "\\`x\\` \\~y\\~ \\|z\\|"},
		{"[t](http://x)", `\[t\]\(http\://x\)`},
		{"<@123>", `\<@` + "​" + `123\>`},
		{"line\nbreak", "line break"},
		{`back\slash`, `back\\slash`},
	}
	for _, tc := range tests {
		if got := escapeMarkdown(tc.in); got != tc.want {
			t.Errorf("escapeMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOlder(t *testing.T) {
	a := core.RemoteLink{UserID: "10", CreatedAt: at(1)}
	b := core.RemoteLink{UserID: "9", CreatedAt: at(2)}
	c := core.RemoteLink{UserID: "9", CreatedAt: at(1)}
	if !older(a, b) || older(b, a) || !older(c, a) || older(a, c) || older(a, a) {
		t.Error("older: earliest first, then the lowest user id as a number")
	}
}
