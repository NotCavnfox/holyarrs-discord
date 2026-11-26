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
	}{{false, "me,privacy"}, {true, "me,privacy,search,request"}}
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
		{"me", func(t *testing.T, f *Feature) { call(t, f, "me", "stranger", nil) }},
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

func TestMe(t *testing.T) {
	r := onRig()
	now := r.clock.Now()
	statuses := []core.RequestStatus{core.RequestPending, core.RequestApproved, core.RequestAvailable, core.RequestDeclined, "weird"}
	doc := map[string][]myRequest{}
	for i := 0; i < 20; i++ { // newest first, as stored
		doc["d1"] = append(doc["d1"], myRequest{
			ID: fmt.Sprint(i), Name: fmt.Sprintf("Mine_%02d", i), Year: 2020, Kind: core.KindMovie,
			Status: statuses[i%len(statuses)], At: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	doc["d2"] = []myRequest{{ID: "x", Name: "Theirs", Status: core.RequestPending, At: now}}
	if err := r.store.Save(storeDoc, doc); err != nil {
		t.Fatal(err)
	}
	last := now.Add(-3 * time.Hour)
	r.ha.people = []core.PersonWatch{
		{UserID: "u1", UserName: "Alice", Movies: 3, Shows: 2, Episodes: 14, Plays: 20, LastWatched: now.Add(-9 * time.Hour)},
		{UserID: "u1", UserName: "alice-tv", Movies: 1, Shows: 1, Episodes: 6, Plays: 7, LastWatched: last},
		{UserID: "u2", UserName: "OtherPerson", Movies: 77777, Plays: 77777},
		{UserID: "", UserName: "Nobody", Movies: 88888, Plays: 88888},
	}

	f := New(r.deps)
	call(t, f, "me", "d1", nil)
	allEphemeral(t, r)
	rp := r.dc.lastReply()
	if rp.Kind != "followup" || len(rp.Msg.Embeds) != 1 {
		t.Fatalf("reply = %+v", rp)
	}
	e := rp.Msg.Embeds[0]
	if e.Title != "Your HolyArrs · Alice" {
		t.Errorf("title = %q", e.Title)
	}
	all := e.Description
	var names []string
	for _, fl := range e.Fields {
		names = append(names, fl.Name)
		all += "\n" + fl.Value
	}
	if got := strings.Join(names, "|"); got != "Pending (3)|Approved (3)|Available (3)|Declined (3)|Other (3)|Your watching" {
		t.Errorf("fields = %s", got)
	}
	if n := strings.Count(all, "Mine"); n != maxMine {
		t.Errorf("%d requests shown, want %d", n, maxMine)
	}
	if strings.Contains(all, "Mine\\_15") || !strings.Contains(all, "Mine\\_14") {
		t.Errorf("should keep the 15 newest: %q", all)
	}
	pending := e.Fields[0].Value
	if strings.Index(pending, "Mine\\_00") > strings.Index(pending, "Mine\\_05") || !strings.Contains(pending, "(2020) · Movie · <t:") {
		t.Errorf("newest first: %q", pending)
	}
	for _, bad := range []string{"Theirs", "OtherPerson", "Nobody", "77777", "88888", "alice-tv"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q must not be shown", bad)
		}
	}
	want := fmt.Sprintf("Movies: 4 · Shows: 3 · Episodes: 20 · Plays: 27\nLast watched <t:%d:R>", last.Unix())
	if got := e.Fields[len(e.Fields)-1].Value; got != want {
		t.Errorf("watching = %q, want %q", got, want)
	}
	if !strings.Contains(e.Description, "15 newest requests of 20") || !strings.Contains(e.Description, textNotListed) {
		t.Errorf("description = %q", e.Description)
	}
}

func TestMeEmptyAndErrors(t *testing.T) {
	cases := []struct {
		name      string
		people    []core.PersonWatch
		peopleErr error
		watch     string
	}{
		{"nothing at all", nil, nil, "Nothing watched yet."},
		{"no last watched", []core.PersonWatch{{UserID: "u1", Plays: 1}}, nil, "Movies: 0 · Shows: 0 · Episodes: 0 · Plays: 1"},
		{"watch totals fail", nil, errors.New("secret internal detail"), "Couldn't load this right now."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := onRig()
			r.ha.people, r.ha.peopleErr = c.people, c.peopleErr
			f := New(r.deps)
			call(t, f, "me", "d1", nil)
			allEphemeral(t, r)
			rp := r.dc.lastReply()
			if rp.Kind != "followup" || len(rp.Msg.Embeds) != 1 {
				t.Fatalf("reply = %+v", rp)
			}
			e := rp.Msg.Embeds[0]
			if len(e.Fields) != 1 || e.Fields[0].Name != "Your watching" || e.Fields[0].Value != c.watch {
				t.Errorf("fields = %+v", e.Fields)
			}
			if !strings.Contains(e.Description, "no requests through the bot yet") || !strings.Contains(e.Description, textNotListed) {
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

	// A new process shows them in /me.
	call(t, New(r.deps), "me", "d1", nil)
	e := r.dc.lastReply().Msg.Embeds[0]
	if len(e.Fields) != 3 || e.Fields[0].Name != "Pending (1)" || e.Fields[1].Name != "Approved (1)" ||
		!strings.Contains(e.Fields[1].Value, "**Second \\(canonical\\)** (2002) · Show") {
		t.Errorf("fields = %+v", e.Fields)
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
	call(t, f, "me", "d1", nil)
	if e := r.dc.lastReply().Msg.Embeds[0]; e.Fields[0].Name != "Approved (1)" {
		t.Errorf("fields = %+v", e.Fields)
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
		value   string
		want    bool
		wantMsg string
	}{
		{"true", true, "will now appear"},
		{"false", false, "no longer appear"},
		{"maybe", false, "true or false"},
		{"", false, "true or false"},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			r := onRig()
			f := New(r.deps)
			call(t, f, "privacy", "d1", map[string]string{"show_watching": c.value})
			rp := r.dc.lastReply()
			if !rp.Ephemeral || !strings.Contains(rp.Msg.Content, c.wantMsg) {
				t.Fatalf("reply = %+v", rp)
			}
			l, _ := r.links.ByDiscord("d1")
			if l.ShowWatching != c.want || l.UserID != "u1" || l.UserName != "Alice" {
				t.Fatalf("link = %+v", l)
			}
		})
	}
	// Turning it off again works too.
	r := onRig()
	f := New(r.deps)
	call(t, f, "privacy", "d1", map[string]string{"show_watching": "true"})
	call(t, f, "privacy", "d1", map[string]string{"show_watching": "false"})
	if l, _ := r.links.ByDiscord("d1"); l.ShowWatching {
		t.Fatal("still showing")
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
