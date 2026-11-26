package selfcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/coretest"
)

func filled() *coretest.FakeHolyArrs {
	ha := coretest.NewFakeHolyArrs()
	ha.UsersVal = []core.User{{ID: "u-secret-1", Name: "Alice Private"}, {ID: "u-secret-2", Name: "Bob Private"}}
	ha.Recent = []core.Title{{ID: 987654321, Name: "Hidden Movie"}}
	ha.PeopleVal = []core.PersonWatch{{UserID: "u-secret-1", UserName: "Alice Private"}, {UserName: "Carol Private"}, {UserName: "Dan Private"}}
	ha.WatchedVal = []core.WatchedTitle{{Title: core.Title{ID: 987654322, Name: "Hidden Show"}}}
	ha.UpcomingVal = []core.CalendarItem{{Title: core.Title{Name: "Hidden Sequel"}, Label: "S01E01 Hidden Episode"}}
	ha.SearchVal = []core.SearchResult{{Ref: "ref-secret", Name: "Hidden Result"}, {Ref: "ref-secret-2", Name: "Hidden Result Two"}}
	ha.LinksVal = []core.RemoteLink{{UserID: "u-secret-1", External: "123456789012345678"}}
	return ha
}

func run(ha core.HolyArrs, discord func(context.Context) error) []string {
	buf := &bytes.Buffer{}
	Run(context.Background(), ha, discord, slog.New(slog.NewTextHandler(buf, nil)))
	return strings.Split(strings.TrimSpace(buf.String()), "\n")
}

// line returns the one log line for check name.
func line(t *testing.T, lines []string, name string) string {
	t.Helper()
	var found []string
	for _, l := range lines {
		if strings.Contains(l, "msg=selfcheck ") && strings.Contains(l, "check="+name+" ") {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("check %s: want 1 line, got %d in:\n%s", name, len(found), strings.Join(lines, "\n"))
	}
	return found[0]
}

func want(t *testing.T, l string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(l+" ", p+" ") {
			t.Errorf("line %q lacks %q", l, p)
		}
	}
}

func okDiscord(context.Context) error { return nil }

func TestAllPass(t *testing.T) {
	ha := filled()
	lines := run(ha, okDiscord)
	if len(lines) != 9 {
		t.Fatalf("want 9 lines, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for name, n := range map[string]int{
		"users": 2, "recently_added": 1, "watch_people": 3, "recently_watched": 1,
		"upcoming": 1, "search": 2, "links": 1,
	} {
		l := line(t, lines, name)
		want(t, l, "ok=true", fmt.Sprintf("n=%d", n))
		if strings.Contains(l, "err=") || !strings.Contains(l, " ms=") {
			t.Errorf("bad line %q", l)
		}
	}
	d := line(t, lines, "discord")
	want(t, d, "ok=true")
	if strings.Contains(d, " n=") {
		t.Errorf("discord line has a count: %q", d)
	}
	want(t, lines[8], `msg="selfcheck done"`, "passed=8", "failed=0")

	if c := ha.CallsTo("Search"); len(c) != 1 || c[0].Args[0] != "matrix" {
		t.Errorf("search calls: %+v", c)
	}
	// Read-only: nothing but the seven reads.
	if got := len(ha.Calls()); got != 7 {
		t.Errorf("want 7 calls, got %d: %+v", got, ha.Calls())
	}
}

func TestNoNamesInLog(t *testing.T) {
	out := strings.Join(run(filled(), okDiscord), "\n")
	for _, bad := range []string{"Private", "Hidden", "secret", "98765432", "123456789012345678", "matrix"} {
		if strings.Contains(out, bad) {
			t.Errorf("log holds %q:\n%s", bad, out)
		}
	}
}

func TestOneFails(t *testing.T) {
	ha := filled()
	ha.SetErr("WatchPeople", errors.New("holyarrs: GET /watch/people: status 500"))
	lines := run(ha, okDiscord)
	l := line(t, lines, "watch_people")
	want(t, l, "ok=false", `err="holyarrs: GET /watch/people: status 500"`)
	if strings.Contains(l, " n=") {
		t.Errorf("failed line has a count: %q", l)
	}
	want(t, line(t, lines, "recently_watched"), "ok=true") // the pass goes on
	want(t, lines[len(lines)-1], "passed=7", "failed=1")
}

func TestLinksUnsupported(t *testing.T) {
	ha := filled()
	ha.SetErr("Links", fmt.Errorf("holyarrs: GET /links: status 404: %w", core.ErrUnsupported))
	lines := run(ha, okDiscord)
	want(t, line(t, lines, "links"), "ok=false", `err="needs a newer HolyArrs"`)
	want(t, lines[len(lines)-1], "passed=7", "failed=1")
}

func TestNilDiscord(t *testing.T) {
	lines := run(filled(), nil)
	want(t, line(t, lines, "discord"), "ok=false", `err="not configured"`)
	want(t, lines[len(lines)-1], "passed=7", "failed=1")
}

func TestErrorsAreScrubbed(t *testing.T) {
	ha := filled()
	ha.SetErr("Users", &url.Error{Op: "Get", URL: "http://host.example/api/users?key=k-secret", Err: errors.New("proxyconnect tcp: dial http://puser:ppass@proxy.example:3128: refused")})
	discord := func(context.Context) error {
		return errors.New("invalid proxy address \"puser:ppass@proxy.example:3128\"\nsecond line")
	}
	out := strings.Join(run(ha, discord), "\n")
	for _, bad := range []string{"puser", "ppass", "k-secret", "host.example", "second line\n"} {
		if strings.Contains(out, bad) {
			t.Errorf("log holds %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "[redacted]@proxy.example:3128") || !strings.Contains(out, "refused") {
		t.Errorf("error text lost its meaning:\n%s", out)
	}
}

type panicky struct{ core.HolyArrs }

func (panicky) Users(context.Context) ([]core.User, error) { panic("boom Alice Private") }

func TestPanicIsAFailedCheck(t *testing.T) {
	lines := run(panicky{filled()}, func(context.Context) error { panic("boom") })
	want(t, line(t, lines, "users"), "ok=false", "err=panic")
	want(t, line(t, lines, "discord"), "ok=false", "err=panic")
	want(t, lines[len(lines)-1], "passed=6", "failed=2")
	if strings.Contains(strings.Join(lines, "\n"), "Alice") {
		t.Error("panic value reached the log")
	}
}

func TestCancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	buf := &bytes.Buffer{}
	ha := filled()
	Run(ctx, ha, okDiscord, slog.New(slog.NewTextHandler(buf, nil)))
	if buf.Len() != 0 || len(ha.Calls()) != 0 {
		t.Errorf("ran after cancel: %q", buf.String())
	}
}
