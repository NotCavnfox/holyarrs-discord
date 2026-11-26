package holyarrs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// widgetServer keeps the body of every home-widget PUT.
func widgetServer(t *testing.T, status int, bodies *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/plugin-hooks/home-widget" {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, string(b))
		if status != http.StatusNoContent {
			fail(w, status, "refused")
			return
		}
		w.WriteHeader(status)
	})
}

func TestSetHomeWidget(t *testing.T) {
	var bodies []string
	c, _ := newTest(t, widgetServer(t, http.StatusNoContent, &bodies))
	err := c.SetHomeWidget(context.Background(), core.Card{Title: "Discord", Rows: []core.CardRow{
		{Text: "Join the server", Sub: "https://discord.gg/abc123"},
		{Text: "Link your account", Sub: "Run /link in Discord, then enter your code under My account", Link: "/settings/account"},
		{Text: "3 members linked"},
	}})
	if err != nil {
		t.Fatalf("SetHomeWidget: %v", err)
	}
	want := `{"title":"Discord","rows":[{"text":"Join the server","sub":"https://discord.gg/abc123"},` +
		`{"text":"Link your account","sub":"Run /link in Discord, then enter your code under My account","link":"/settings/account"},` +
		`{"text":"3 members linked"}]}`
	if len(bodies) != 1 || bodies[0] != want {
		t.Fatalf("body\n got %v\nwant %s", bodies, want)
	}
}

func TestSetHomeWidgetFitsServerRules(t *testing.T) {
	var bodies []string
	c, _ := newTest(t, widgetServer(t, http.StatusNoContent, &bodies))
	long := strings.Repeat("é", 250)
	rows := []core.CardRow{
		{Text: "<b>bold</b>", Sub: " a > b ", Link: "https://evil.example/x"},
		{Text: long, Sub: long, Link: "/api/v1/users"},
		{Text: " <> ", Sub: "dropped: no text left"},
		{Text: "up", Link: "/a/../b"},
		{Text: "scheme", Link: "//evil.example"},
		{Text: "upper", Link: "/Settings"},
		{Text: "ok", Link: "/title/12?tab=a&b=c.d_e-f"},
	}
	for range 30 {
		rows = append(rows, core.CardRow{Text: "filler"})
	}
	if err := c.SetHomeWidget(context.Background(), core.Card{Title: " <Dis>cord ", Rows: rows}); err != nil {
		t.Fatalf("SetHomeWidget: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("calls = %d", len(bodies))
	}
	var got struct {
		Title string `json:"title"`
		Rows  []struct {
			Text string `json:"text"`
			Sub  string `json:"sub"`
			Link string `json:"link"`
		} `json:"rows"`
	}
	if err := jsonStrict(bodies[0], &got); err != nil {
		t.Fatalf("body is not the card shape: %v\n%s", err, bodies[0])
	}
	if strings.ContainsAny(bodies[0], "<>") || strings.Contains(bodies[0], `<`) || strings.Contains(bodies[0], `>`) {
		t.Errorf("markup characters were sent: %s", bodies[0])
	}
	if got.Title != "Discord" {
		t.Errorf("title = %q", got.Title)
	}
	if len(got.Rows) != 20 {
		t.Fatalf("rows = %d, want 20", len(got.Rows))
	}
	if r := got.Rows[0]; r.Text != "bbold/b" || r.Sub != "a  b" || r.Link != "" {
		t.Errorf("row 1 = %+v", r)
	}
	if r := got.Rows[1]; utf8.RuneCountInString(r.Text) != 200 || utf8.RuneCountInString(r.Sub) != 200 || r.Link != "" {
		t.Errorf("row 2 not cut to 200 characters or kept an /api link: %d %d %q",
			utf8.RuneCountInString(r.Text), utf8.RuneCountInString(r.Sub), r.Link)
	}
	for i, text := range []string{"up", "scheme", "upper"} {
		if r := got.Rows[2+i]; r.Text != text || r.Link != "" {
			t.Errorf("row %q = %+v, want no link", text, r)
		}
	}
	if r := got.Rows[5]; r.Text != "ok" || r.Link != "/title/12?tab=a&b=c.d_e-f" {
		t.Errorf("good link row = %+v", r)
	}
}

func TestSetHomeWidgetEmpty(t *testing.T) {
	var bodies []string
	c, _ := newTest(t, widgetServer(t, http.StatusNoContent, &bodies))
	if err := c.SetHomeWidget(context.Background(), core.Card{Title: " <> "}); err == nil {
		t.Error("a card without a title was sent")
	}
	if len(bodies) != 0 {
		t.Fatalf("calls = %d, want 0", len(bodies))
	}
	if err := c.SetHomeWidget(context.Background(), core.Card{Title: "Discord"}); err != nil {
		t.Fatalf("SetHomeWidget: %v", err)
	}
	if len(bodies) != 1 || bodies[0] != `{"title":"Discord","rows":[]}` {
		t.Fatalf("bodies = %v", bodies)
	}
}

func TestSetHomeWidgetErrors(t *testing.T) {
	var bodies []string
	c, _ := newTest(t, widgetServer(t, http.StatusForbidden, &bodies))
	err := c.SetHomeWidget(context.Background(), core.Card{Title: "Discord"})
	if !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("403 = %v, want ErrForbidden", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error shows the token: %v", err)
	}

	c, _ = newTest(t, widgetServer(t, http.StatusBadRequest, &bodies))
	if err := c.SetHomeWidget(context.Background(), core.Card{Title: "Discord"}); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("400 = %v", err)
	}
}

// jsonStrict decodes like HolyArrs does: unknown fields are an error.
func jsonStrict(s string, v any) error {
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
