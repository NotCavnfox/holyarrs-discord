package requests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

func onRig() *rig {
	r := newRig(core.Config{EnableRequests: true})
	_ = r.links.Put(core.Link{DiscordID: "d1", UserID: "u1", UserName: "Alice"})
	return r
}

func cmd(t *testing.T, f *Feature, name string) core.Command {
	t.Helper()
	for _, c := range f.Commands() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no command %q", name)
	return core.Command{}
}

func call(t *testing.T, f *Feature, name, user string, opts map[string]string) {
	t.Helper()
	in := &core.Interaction{UserID: user, Command: name, Options: opts}
	if err := cmd(t, f, name).Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func press(t *testing.T, f *Feature, user, customID string) {
	t.Helper()
	if err := f.HandleComponent(context.Background(), &core.Interaction{UserID: user, CustomID: customID}); err != nil {
		t.Fatal(err)
	}
}

func allEphemeral(t *testing.T, r *rig) {
	t.Helper()
	_, _, _, _, replies := r.dc.snap()
	for _, rp := range replies {
		if !rp.Ephemeral {
			t.Errorf("reply %q (%s) is not ephemeral", rp.Msg.Content, rp.Kind)
		}
	}
}

func TestCommandsByConfig(t *testing.T) {
	cases := []struct {
		enabled bool
		want    string
	}{{false, "status,privacy"}, {true, "status,privacy,search,request"}}
	for _, c := range cases {
		f := New(newRig(core.Config{EnableRequests: c.enabled}).deps)
		var names []string
		for _, cm := range f.Commands() {
			names = append(names, cm.Name)
		}
		if got := strings.Join(names, ","); got != c.want || f.Name() != "requests" {
			t.Errorf("enabled=%v: commands %q", c.enabled, got)
		}
	}
}

func TestNotLinkedGetsLinkHelp(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, f *Feature)
	}{
		{"status", func(t *testing.T, f *Feature) { call(t, f, "status", "stranger", nil) }},
		{"privacy", func(t *testing.T, f *Feature) {
			call(t, f, "privacy", "stranger", map[string]string{"show_watching": "true"})
		}},
		{"search", func(t *testing.T, f *Feature) { call(t, f, "search", "stranger", map[string]string{"query": "x"}) }},
		{"request", func(t *testing.T, f *Feature) { call(t, f, "request", "stranger", map[string]string{"title": "x"}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			f := New(r.deps)
			c.run(t, f)
			rp := r.dc.lastReply()
			if rp.Kind != "respond" || !rp.Ephemeral || !strings.Contains(rp.Msg.Content, "/link") {
				t.Fatalf("reply = %+v", rp)
			}
			if r.ha.count("search")+r.ha.count("people")+r.ha.count("create") != 0 {
				t.Error("HolyArrs was called for an unlinked member")
			}
		})
	}
}

func TestStatusInProgress(t *testing.T) {
	r := onRig()
	now := r.clock.Now()
	doc := map[string][]myRequest{"d1": { // newest first, as stored
		{ID: "1", Name: "Waiting_One", Year: 2021, Status: core.RequestPending, At: now},
		{ID: "2", Name: "Ready", Year: 2020, Status: core.RequestAvailable, At: now.Add(-time.Hour)},
		{ID: "3", Name: "Approved One", Status: core.RequestApproved, At: now.Add(-2 * time.Hour)},
		{ID: "4", Name: "Refused", Status: core.RequestDeclined, At: now.Add(-3 * time.Hour)},
		{ID: "5", Name: "Odd", Status: "weird", At: now.Add(-4 * time.Hour)},
	}, "d2": {{ID: "x", Name: "Theirs", Status: core.RequestPending, At: now}}}
	if err := r.store.Save(storeDoc, doc); err != nil {
		t.Fatal(err)
	}
	call(t, New(r.deps), "status", "d1", nil)
	allEphemeral(t, r)
	rp := r.dc.lastReply()
	if rp.Kind != "respond" || len(rp.Msg.Embeds) != 1 {
		t.Fatalf("reply = %+v", rp)
	}
	e := rp.Msg.Embeds[0]
	want := "**Waiting\\_One** (2021) · Waiting for approval\n" +
		"**Approved One** · Approved, not on the server yet\n" +
		"**Odd** · In progress"
	if e.Title != "Your requests · Alice" || e.Description != want || e.Footer != textNotListed || len(e.Fields) != 0 {
		t.Errorf("embed = %+v", e)
	}
	if r.ha.count("people") != 0 {
		t.Error("/status asked HolyArrs for watch totals")
	}
}

func TestStatusNothingInProgress(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var done []myRequest
	for i := 0; i < 5; i++ { // newest first, as stored
		done = append(done, myRequest{ID: fmt.Sprint(i), Name: fmt.Sprintf("Done %d", i), Year: 2010 + i,
			Status: core.RequestAvailable, At: now.Add(-time.Duration(i) * time.Hour)})
	}
	cases := []struct {
		name string
		mine []myRequest
		want string
	}{
		{"last three available", append([]myRequest{{ID: "d", Name: "Refused", Status: core.RequestDeclined, At: now}}, done...),
			"Nothing is in progress. Your last available requests:\n" +
				"**Done 0** (2010) · Available\n**Done 1** (2011) · Available\n**Done 2** (2012) · Available"},
		{"only declined", []myRequest{{ID: "d", Name: "Refused", Status: core.RequestDeclined, At: now}},
			"Nothing is in progress. Use /request to ask for something."},
		{"nothing at all", nil, "Nothing is in progress. Use /request to ask for something."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			if err := r.store.Save(storeDoc, map[string][]myRequest{"d1": c.mine}); err != nil {
				t.Fatal(err)
			}
			call(t, New(r.deps), "status", "d1", nil)
			allEphemeral(t, r)
			e := r.dc.lastReply().Msg.Embeds[0]
			if e.Description != c.want || e.Footer != textNotListed {
				t.Errorf("description = %q", e.Description)
			}
		})
	}
}

func storedDoc(t *testing.T, r *rig) map[string][]myRequest {
	t.Helper()
	var doc map[string][]myRequest
	if err := r.store.Load(storeDoc, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// request runs /search for one result as d1 and presses its button.
func request(t *testing.T, r *rig, f *Feature, res core.SearchResult) {
	t.Helper()
	r.ha.mu.Lock()
	r.ha.results = []core.SearchResult{res, {Ref: "zz", Name: "Filler"}}
	r.ha.mu.Unlock()
	call(t, f, "search", "d1", map[string]string{"query": "q"})
	press(t, f, "d1", r.dc.lastReply().Msg.Rows[0][0].CustomID)
}

func TestRequestsAreRemembered(t *testing.T) {
	r := onRig()
	f := New(r.deps)
	request(t, r, f, core.SearchResult{Ref: "a", Name: "First", Year: 2001, Kind: core.KindMovie})
	r.clock.Add(time.Hour)
	created := r.clock.Now().Add(-time.Minute)
	r.ha.createResp = &core.Request{
		ID: "ask-9", Title: core.Title{ID: 44, Name: "Second (canonical)", Year: 2002, Kind: core.KindShow},
		Status: core.RequestApproved, CreatedAt: created,
	}
	request(t, r, f, core.SearchResult{Ref: "b", Name: "Second", Year: 1999, Kind: core.KindMovie})
	if got := r.dc.lastReply().Msg.Content; !strings.Contains(got, "approved straight away") {
		t.Errorf("reply = %q", got)
	}

	doc := storedDoc(t, r)
	list := doc["d1"]
	if len(doc) != 1 || len(list) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	if want := (myRequest{ID: "ask-9", Name: "Second (canonical)", Year: 2002, Kind: core.KindShow, TitleID: 44, Status: core.RequestApproved, At: created}); !list[0].At.Equal(want.At) ||
		list[0].ID != want.ID || list[0].Name != want.Name || list[0].Year != want.Year || list[0].Kind != want.Kind || list[0].TitleID != want.TitleID || list[0].Status != want.Status {
		t.Errorf("newest = %+v, want %+v", list[0], want)
	}
	if list[1].ID != "new-1" || list[1].Name != "First" || list[1].Year != 2001 || list[1].Kind != core.KindMovie ||
		list[1].Status != core.RequestPending || !list[1].At.Equal(r.clock.Now().Add(-time.Hour)) {
		t.Errorf("older = %+v", list[1])
	}

	// Asking for the same request again does not list it twice.
	request(t, r, f, core.SearchResult{Ref: "b", Name: "Second"})
	if list := storedDoc(t, r)["d1"]; len(list) != 2 || list[0].ID != "ask-9" || !list[0].At.Equal(created) {
		t.Errorf("after asking again: %+v", list)
	}

	// A new process shows them in /status.
	call(t, New(r.deps), "status", "d1", nil)
	e := r.dc.lastReply().Msg.Embeds[0]
	if want := "**Second \\(canonical\\)** (2002) · Approved, not on the server yet\n**First** (2001) · Waiting for approval"; e.Description != want {
		t.Errorf("description = %q", e.Description)
	}
}

func TestStoredRequestsAreCapped(t *testing.T) {
	r := onRig()
	f := New(r.deps)
	for i := 0; i < maxStored+5; i++ {
		f.remember("d1", myRequest{ID: fmt.Sprint(i), Name: "T", Status: core.RequestPending})
	}
	list := storedDoc(t, r)["d1"]
	if len(list) != maxStored || list[0].ID != fmt.Sprint(maxStored+4) || list[maxStored-1].ID != "5" {
		t.Fatalf("len = %d, first = %+v, last = %+v", len(list), list[0], list[len(list)-1])
	}
}

func TestAlreadyInLibrary(t *testing.T) {
	r := onRig()
	f := New(r.deps)
	r.ha.createErr = fmt.Errorf("409: %w", core.ErrAlreadyAvailable)
	request(t, r, f, core.SearchResult{Ref: "a", Name: "Here"})
	allEphemeral(t, r)
	rp := r.dc.lastReply()
	if rp.Kind != "followup" || rp.Msg.Content != "That's already in the library." {
		t.Fatalf("reply = %+v", rp)
	}
	if doc := storedDoc(t, r); len(doc) != 0 {
		t.Errorf("recorded a request that was not made: %+v", doc)
	}
}

func TestRequestEvents(t *testing.T) {
	seed := func() map[string][]myRequest {
		return map[string][]myRequest{
			"d1": {
				{ID: "r1", Name: "Dune_2", Year: 2024, Status: core.RequestPending},
				{ID: "r2", Name: "Seven", TitleID: 7, Status: core.RequestPending},
			},
			"d2": {{ID: "r1", Name: "Dune_2", Year: 2024, Status: core.RequestPending}},
			"d3": {{ID: "", Name: "No id", TitleID: 7, Status: core.RequestApproved}},
		}
	}
	type want struct{ d1r1, d1r2, d2r1, d3 core.RequestStatus }
	p, ap, av, de := core.RequestPending, core.RequestApproved, core.RequestAvailable, core.RequestDeclined
	cases := []struct {
		name    string
		ev      core.Event
		want    want
		wantDMs []string
		text    string
	}{
		{"approved by request id", core.Event{Type: core.EventRequestApproved, RequestID: "r1"}, want{ap, p, ap, ap}, []string{"d1", "d2"}, `Your request for **Dune\_2** (2024) was approved.`},
		{"declined by request id", core.Event{Type: core.EventRequestDeclined, RequestID: "r1", TitleID: 99}, want{de, p, de, ap}, []string{"d1", "d2"}, `Your request for **Dune\_2** (2024) was declined.`},
		{"available never DMs", core.Event{Type: core.EventRequestAvailable, RequestID: "r1", TitleID: 5}, want{av, p, av, ap}, nil, ""},
		{"available by title id", core.Event{Type: core.EventRequestAvailable, TitleID: 7}, want{p, av, p, av}, nil, ""},
		{"declined by title id", core.Event{Type: core.EventRequestDeclined, TitleID: 7}, want{p, de, p, de}, []string{"d1", "d3"}, ""},
		{"request id wins over title id", core.Event{Type: core.EventRequestApproved, RequestID: "other", TitleID: 7}, want{p, p, p, ap}, nil, ""},
		{"unknown request", core.Event{Type: core.EventRequestApproved, RequestID: "nope"}, want{p, p, p, ap}, nil, ""},
		{"no ids", core.Event{Type: core.EventRequestApproved}, want{p, p, p, ap}, nil, ""},
		{"created is ignored", core.Event{Type: core.EventRequestCreated, RequestID: "r1"}, want{p, p, p, ap}, nil, ""},
		{"title added is ignored", core.Event{Type: core.EventTitleAdded, TitleID: 7}, want{p, p, p, ap}, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			if err := r.store.Save(storeDoc, seed()); err != nil {
				t.Fatal(err)
			}
			r.dc.dmErr["d2"] = fmt.Errorf("closed: %w", core.ErrForbidden) // ignored
			f := New(r.deps)
			ctx := context.Background()
			f.handleEvent(ctx, c.ev)
			f.handleEvent(ctx, c.ev)           // replayed
			New(r.deps).handleEvent(ctx, c.ev) // replayed after a restart

			doc := storedDoc(t, r)
			got := want{doc["d1"][0].Status, doc["d1"][1].Status, doc["d2"][0].Status, doc["d3"][0].Status}
			if got != c.want {
				t.Errorf("statuses = %+v, want %+v", got, c.want)
			}
			_, _, dms, _, _ := r.dc.snap()
			var to []string
			for _, d := range dms {
				to = append(to, d.Channel)
			}
			wantTo := []string{}
			for _, id := range c.wantDMs {
				if id != "d2" { // closed DMs record nothing in the fake
					wantTo = append(wantTo, id)
				}
			}
			if strings.Join(to, ",") != strings.Join(wantTo, ",") {
				t.Fatalf("dms to %v, want %v", to, wantTo)
			}
			if c.text != "" && dms[0].Msg.Content != c.text {
				t.Errorf("dm = %q, want %q", dms[0].Msg.Content, c.text)
			}
			if c.ev.RequestID == "r1" && c.ev.TitleID != 0 && doc["d1"][0].TitleID != c.ev.TitleID {
				t.Errorf("title id not learned from the event: %+v", doc["d1"][0])
			}
		})
	}
}

func TestReplayedApprovalDoesNotUndoAvailable(t *testing.T) {
	r := onRig()
	_ = r.store.Save(storeDoc, map[string][]myRequest{"d1": {{ID: "r1", Name: "X", Status: core.RequestAvailable}}})
	f := New(r.deps)
	f.handleEvent(context.Background(), core.Event{Type: core.EventRequestApproved, RequestID: "r1"})
	if st := storedDoc(t, r)["d1"][0].Status; st != core.RequestAvailable {
		t.Errorf("status = %s", st)
	}
	if _, _, dms, _, _ := r.dc.snap(); len(dms) != 0 {
		t.Errorf("dms = %+v", dms)
	}
}

func TestRunFollowsEvents(t *testing.T) {
	r := onRig()
	_ = r.store.Save(storeDoc, map[string][]myRequest{"d1": {{ID: "r1", Name: "X", Status: core.RequestPending}}})
	f := New(r.deps)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	r.ev.ch <- core.Event{Type: core.EventRequestApproved, RequestID: "r1"}
	if !waitFor(func() bool { _, _, dms, _, _ := r.dc.snap(); return len(dms) == 1 }) {
		t.Fatal("no DM after an approved event")
	}
	call(t, f, "status", "d1", nil)
	if e := r.dc.lastReply().Msg.Embeds[0]; e.Description != "**X** · Approved, not on the server yet" {
		t.Errorf("description = %q", e.Description)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func results() []core.SearchResult {
	return []core.SearchResult{
		{Ref: strings.Repeat("r", 300), Name: "Dune *Part* Two", Year: 2024, Kind: core.KindMovie, Overview: strings.Repeat("o", 500)},
		{Ref: "b", Name: "Dune", Year: 2021, Kind: core.KindMovie, InLibrary: true},
		{Ref: "c", Name: strings.Repeat("Long", 60), Year: 1984},
		{Ref: "d", Name: "D"}, {Ref: "e", Name: "E"}, {Ref: "f", Name: "F"}, {Ref: "g", Name: "G"},
	}
}

func TestSearchListsResultsWithButtons(t *testing.T) {
	r := onRig()
	r.ha.results = results()
	f := New(r.deps)
	call(t, f, "search", "d1", map[string]string{"query": "dune"})
	allEphemeral(t, r)
	rp := r.dc.lastReply()
	if rp.Kind != "followup" || len(rp.Msg.Embeds) != 1 || len(rp.Msg.Rows) != 1 {
		t.Fatalf("reply = %+v", rp)
	}
	desc := rp.Msg.Embeds[0].Description
	if !strings.Contains(desc, `Dune \*Part\* Two`) || !strings.Contains(desc, "already in the library") ||
		strings.Contains(desc, "**6.**") || !strings.Contains(desc, "**5.**") {
		t.Errorf("description = %q", desc)
	}
	if utf8.RuneCountInString(desc) > maxEmbedDesc {
		t.Errorf("description too long")
	}
	row := rp.Msg.Rows[0]
	if len(row) != 4 { // five results, one already in the library
		t.Fatalf("buttons = %d", len(row))
	}
	for _, b := range row {
		if !strings.HasPrefix(b.CustomID, "requests:req:") || len(b.CustomID) > 100 || utf8.RuneCountInString(b.Label) > 80 {
			t.Errorf("button = %+v", b)
		}
	}
	if !strings.HasSuffix(row[1].Label, "…") {
		t.Errorf("long label not truncated: %q", row[1].Label)
	}

	// Pressing sends the whole original result back to HolyArrs.
	press(t, f, "d1", row[0].CustomID)
	allEphemeral(t, r)
	if len(r.ha.created) != 1 || r.ha.created[0].As != "u1" || r.ha.created[0].What.Ref != results()[0].Ref {
		t.Fatalf("created = %+v", r.ha.created)
	}
	_, _, _, _, replies := r.dc.snap()
	last2 := replies[len(replies)-2:]
	if last2[0].Kind != "defer" || last2[1].Kind != "followup" || !strings.Contains(last2[1].Msg.Content, "Requested") {
		t.Fatalf("replies = %+v", last2)
	}
	// A second press of the same button does not request twice.
	press(t, f, "d1", row[0].CustomID)
	if len(r.ha.created) != 1 || r.dc.lastReply().Msg.Content != textExpired {
		t.Fatalf("double press: created=%d reply=%q", len(r.ha.created), r.dc.lastReply().Msg.Content)
	}
}

func TestSearchEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		results []core.SearchResult
		err     error
		query   string
		want    string
		rows    int
	}{
		{"nothing found", nil, nil, "x*y", `Nothing found for x\*y.`, 0},
		{"search fails", nil, errors.New("boom"), "x", textTrouble, 0},
		{"rate limited", nil, core.ErrRateLimited, "x", "HolyArrs is busy right now. Try again in a minute.", 0},
		{"empty query", nil, nil, "  ", "Tell me what to look for.", 0},
		{"all in library", []core.SearchResult{{Ref: "a", Name: "A", InLibrary: true}}, nil, "a", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			r.ha.results, r.ha.searchErr = c.results, c.err
			f := New(r.deps)
			call(t, f, "search", "d1", map[string]string{"query": c.query})
			allEphemeral(t, r)
			rp := r.dc.lastReply()
			if rp.Msg.Content != c.want || len(rp.Msg.Rows) != c.rows {
				t.Fatalf("reply = %+v", rp)
			}
		})
	}
}

func TestRequestCommand(t *testing.T) {
	cases := []struct {
		name        string
		results     []core.SearchResult
		wantConfirm bool
		wantButtons int
	}{
		{"single not in library confirms", []core.SearchResult{{Ref: "a", Name: "Only", Year: 2020}}, true, 1},
		{"single in library lists", []core.SearchResult{{Ref: "a", Name: "Only", InLibrary: true}}, false, 0},
		{"several list", []core.SearchResult{{Ref: "a", Name: "A"}, {Ref: "b", Name: "B"}}, false, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			r.ha.results = c.results
			f := New(r.deps)
			call(t, f, "request", "d1", map[string]string{"title": "only"})
			allEphemeral(t, r)
			rp := r.dc.lastReply()
			n := 0
			for _, row := range rp.Msg.Rows {
				n += len(row)
			}
			if n != c.wantButtons {
				t.Fatalf("buttons = %d", n)
			}
			confirm := n == 1 && rp.Msg.Rows[0][0].Label == "Confirm request"
			if confirm != c.wantConfirm {
				t.Fatalf("confirm = %v, reply = %+v", confirm, rp)
			}
			if c.wantConfirm {
				if rp.Msg.Embeds[0].Title != "Request Only (2020)?" {
					t.Errorf("title = %q", rp.Msg.Embeds[0].Title)
				}
				press(t, f, "d1", rp.Msg.Rows[0][0].CustomID)
				if len(r.ha.created) != 1 {
					t.Fatal("confirm did not request")
				}
			}
		})
	}
}

func TestButtonRules(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(r *rig, f *Feature)
		presser     string
		id          string // "" = the real button
		want        string
		wantCreated int
		wantKind    string
	}{
		{"owner succeeds", nil, "d1", "", "Requested **Thing**. It is waiting for approval.", 1, "followup"},
		{"another member is refused", nil, "d2", "", textNotYours, 0, "respond"},
		{"expired key", func(r *rig, f *Feature) { r.clock.Add(15*time.Minute + time.Second) }, "d1", "", textExpired, 0, "respond"},
		{"just before expiry", func(r *rig, f *Feature) { r.clock.Add(15*time.Minute - time.Second) }, "d1", "", "Requested **Thing**. It is waiting for approval.", 1, "followup"},
		{"unknown key", nil, "d1", "requests:req:nope", textExpired, 0, "respond"},
		{"malformed id", nil, "d1", "requests:other", "That button isn't in use any more.", 0, "respond"},
		{"limit reached", func(r *rig, f *Feature) { r.ha.createErr = fmt.Errorf("429: %w", core.ErrLimit) }, "d1", "", "You've reached your request limit.", 0, "followup"},
		{"HolyArrs says not linked", func(r *rig, f *Feature) { r.ha.createErr = core.ErrNotLinked }, "d1", "", textNotLinked, 0, "followup"},
		{"other error is friendly", func(r *rig, f *Feature) { r.ha.createErr = errors.New("stack trace") }, "d1", "", textTrouble, 0, "followup"},
		{"link removed since search", func(r *rig, f *Feature) { _ = r.links.Remove("d1") }, "d1", "", textNotLinked, 0, "respond"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			_ = r.links.Put(core.Link{DiscordID: "d2", UserID: "u2"})
			r.ha.results = []core.SearchResult{{Ref: "ref", Name: "Thing"}, {Ref: "ref2", Name: "Other"}}
			f := New(r.deps)
			call(t, f, "search", "d1", map[string]string{"query": "thing"})
			id := r.dc.lastReply().Msg.Rows[0][0].CustomID
			if c.id != "" {
				id = c.id
			}
			if c.setup != nil {
				c.setup(r, f)
			}
			press(t, f, c.presser, id)
			allEphemeral(t, r)
			rp := r.dc.lastReply()
			if rp.Msg.Content != c.want || rp.Kind != c.wantKind {
				t.Fatalf("reply = %s %q, want %s %q", rp.Kind, rp.Msg.Content, c.wantKind, c.want)
			}
			if len(r.ha.created) != c.wantCreated {
				t.Fatalf("created = %d", len(r.ha.created))
			}
		})
	}
}

func TestRefusedPressLeavesButtonForOwner(t *testing.T) {
	r := onRig()
	_ = r.links.Put(core.Link{DiscordID: "d2", UserID: "u2"})
	r.ha.results = []core.SearchResult{{Ref: "ref", Name: "Thing"}, {Ref: "x", Name: "Y"}}
	f := New(r.deps)
	call(t, f, "search", "d1", map[string]string{"query": "thing"})
	id := r.dc.lastReply().Msg.Rows[0][0].CustomID
	press(t, f, "d2", id)
	r.ha.createErr = errors.New("flaky")
	press(t, f, "d1", id) // fails, can retry
	r.ha.createErr = nil
	press(t, f, "d1", id)
	if len(r.ha.created) != 1 || r.ha.created[0].As != "u1" {
		t.Fatalf("created = %+v", r.ha.created)
	}
}

func TestCacheIsBounded(t *testing.T) {
	r := onRig()
	f := New(r.deps)
	f.maxCache = 10
	first := f.put("d1", core.SearchResult{Ref: "0"})
	for i := 1; i < 25; i++ {
		r.clock.Add(time.Second)
		f.put("d1", core.SearchResult{Ref: fmt.Sprint(i)})
	}
	if len(f.cache) != 10 {
		t.Fatalf("cache size = %d", len(f.cache))
	}
	if _, res := f.take(first, "d1"); res != takeExpired {
		t.Fatal("oldest entry should have been evicted")
	}
	r.clock.Add(16 * time.Minute)
	f.put("d1", core.SearchResult{Ref: "new"})
	if len(f.cache) != 1 {
		t.Fatalf("expired entries not pruned: %d", len(f.cache))
	}
}

func TestDisabledButtons(t *testing.T) {
	r := newRig(core.Config{EnableRequests: false})
	_ = r.links.Put(core.Link{DiscordID: "d1", UserID: "u1"})
	f := New(r.deps)
	key := f.put("d1", core.SearchResult{Ref: "a"})
	press(t, f, "d1", f.customID(key))
	if r.dc.lastReply().Msg.Content != textDisabled || len(r.ha.created) != 0 {
		t.Fatal("disabled feature made a request")
	}
}

func TestPrivacy(t *testing.T) {
	cases := []struct {
		name       string
		opts       map[string]string
		start      core.Link
		wantShow   bool
		wantAlerts bool
		wantMsg    []string
	}{
		{"show on", map[string]string{"show_watching": "true"}, core.Link{}, true, true, []string{"Done.", "shows next to what you watch", "Alerts are on"}},
		{"show off", map[string]string{"show_watching": "false"}, core.Link{ShowWatching: true}, false, true, []string{"Done.", "hidden from watch activity"}},
		{"alerts off", map[string]string{"alerts": "off"}, core.Link{ShowWatching: true}, true, false, []string{"Done.", "Alerts are off", "shows next to what you watch"}},
		{"alerts on", map[string]string{"alerts": "on"}, core.Link{AlertsOff: true}, false, true, []string{"Done.", "Alerts are on"}},
		{"both", map[string]string{"show_watching": "on", "alerts": "off"}, core.Link{}, true, false, []string{"Done.", "shows next", "Alerts are off"}},
		{"neither shows the defaults", nil, core.Link{}, false, true, []string{"hidden from watch activity", "Alerts are on", "/privacy alerts"}},
		{"neither shows what was chosen", map[string]string{}, core.Link{ShowWatching: true, AlertsOff: true}, true, false, []string{"shows next", "Alerts are off"}},
		{"bad show_watching", map[string]string{"show_watching": "maybe", "alerts": "false"}, core.Link{}, false, true, []string{"on or off for show_watching"}},
		{"bad alerts", map[string]string{"alerts": "maybe"}, core.Link{}, false, true, []string{"on or off for alerts"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			l := c.start
			l.DiscordID, l.UserID, l.UserName = "d1", "u1", "Alice"
			_ = r.links.Put(l)
			f := New(r.deps)
			call(t, f, "privacy", "d1", c.opts)
			rp := r.dc.lastReply()
			for _, w := range c.wantMsg {
				if !rp.Ephemeral || !strings.Contains(rp.Msg.Content, w) {
					t.Errorf("reply %q lacks %q", rp.Msg.Content, w)
				}
			}
			if len(c.opts) == 0 && strings.Contains(rp.Msg.Content, "Done.") {
				t.Errorf("nothing was changed, but the reply says so: %q", rp.Msg.Content)
			}
			got, _ := r.links.ByDiscord("d1")
			if got.ShowWatching != c.wantShow || got.AlertsOff == c.wantAlerts || got.UserID != "u1" || got.UserName != "Alice" {
				t.Fatalf("link = %+v", got)
			}
		})
	}
}

func TestPrivacyOptionsAreOptional(t *testing.T) {
	c := cmd(t, New(onRig().deps), "privacy")
	var names []string
	for _, o := range c.Options {
		names = append(names, o.Name)
		if o.Required || o.Type != core.OptString || strings.Join(o.Choices, ",") != "on,off" {
			t.Errorf("option %s: required=%v type=%v choices=%v", o.Name, o.Required, o.Type, o.Choices)
		}
	}
	if got := strings.Join(names, ","); got != "show_watching,alerts" {
		t.Errorf("options = %s", got)
	}
}

func TestRunWaits(t *testing.T) {
	f := New(onRig().deps)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

// The feature subscribes when it is built, once, so an event published
// before Run starts is handled.
func TestSubscribesInNew(t *testing.T) {
	r := onRig()
	ev := &countingEvents{ch: make(chan core.Event, 8)}
	r.deps.Events = ev
	if err := r.store.Save(storeDoc, map[string][]myRequest{
		"d1": {{ID: "r1", Name: "Dune", Status: core.RequestPending}},
	}); err != nil {
		t.Fatal(err)
	}
	f := New(r.deps)
	if got := ev.names(); len(got) != 1 || got[0] != "requests" {
		t.Fatalf("subscriptions after New = %v", got)
	}
	ev.ch <- core.Event{Type: core.EventRequestApproved, RequestID: "r1"} // before Run
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	if !waitFor(func() bool { _, _, dms, _, _ := r.dc.snap(); return len(dms) == 1 }) {
		t.Fatal("the early event was lost")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := ev.names(); len(got) != 1 {
		t.Fatalf("Run subscribed again: %v", got)
	}
}

type countingEvents struct {
	mu   sync.Mutex
	subs []string
	ch   chan core.Event
}

func (e *countingEvents) Subscribe(name string) <-chan core.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subs = append(e.subs, name)
	return e.ch
}

func (e *countingEvents) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.subs...)
}
