package dashboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
	"github.com/NotCavnfox/holyarrs-discord/internal/postergrid"
)

func onCfg() core.Config {
	return core.Config{EnableDashboard: true, DashboardChannel: "chan", RefreshEvery: time.Hour}
}

func seed(r *rig) {
	now := r.clock.Now()
	r.ha.recent = []core.Title{
		{ID: 1, Name: "Old One", Year: 1999, Kind: core.KindMovie, AddedAt: now.Add(-48 * time.Hour), PosterURL: "https://img/old.jpg"},
		{ID: 2, Name: "New *One*", Year: 2024, Kind: core.KindShow, AddedAt: now.Add(-time.Hour), PosterURL: "https://img/new.jpg"},
	}
	r.ha.watched = []core.WatchedTitle{
		{Title: core.Title{ID: 2, Name: "New *One*", Year: 2024}, LastWatched: now.Add(-2 * time.Hour)},
		{Title: core.Title{ID: 1, Name: "Old One"}, LastWatched: now.Add(-time.Hour)},
		{Title: core.Title{ID: 3, Name: "Third"}},
	}
	r.ha.people = []core.PersonWatch{
		{UserID: "u2", UserName: "Bob", Plays: 9},
		{UserID: "u1", UserName: "Alice", Plays: 3},
		{UserID: "u1", UserName: "alice-on-jellyfin", Plays: 2}, // second account, same person
		{UserID: "", UserName: "Ghost", Plays: 20},
		{UserID: "u9", UserName: "Nolink", Plays: 2},
		{UserID: "u3", UserName: "Carol", Plays: 1},
		{UserID: "u4", UserName: "Idle", Plays: 0},
	}
	r.ha.upcoming = []core.CalendarItem{
		{Title: core.Title{ID: 5, Name: "Later"}, Label: "S01E02", Date: now.Add(72 * time.Hour)},
		{Title: core.Title{ID: 6, Name: "Soon"}, Date: now.Add(24 * time.Hour)},
		{Title: core.Title{ID: 7, Name: "Past"}, Date: now.Add(-72 * time.Hour)},
	}
	_ = r.links.Put(core.Link{DiscordID: "1001", UserID: "u1", UserName: "Alice", ShowWatching: true})
	_ = r.links.Put(core.Link{DiscordID: "1002", UserID: "u2", UserName: "Bob", ShowWatching: false})
	_ = r.links.Put(core.Link{DiscordID: "1003", UserID: "u3", UserName: "Carol", ShowWatching: true})
}

func byTitle(msgs []sentMsg) map[string]core.Embed {
	out := map[string]core.Embed{}
	for _, m := range msgs {
		out[m.Msg.Embeds[0].Title] = m.Msg.Embeds[0]
	}
	return out
}

func TestTilesContentAndPrivacy(t *testing.T) {
	r := newRig(onCfg())
	seed(r)
	f := New(r.deps)
	if err := f.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	sends, edits, _, pins, _ := r.dc.snap()
	if len(sends) != 4 || len(pins) != 4 || len(edits) != 0 {
		t.Fatalf("sends=%d pins=%d edits=%d", len(sends), len(pins), len(edits))
	}
	tiles := byTitle(sends)

	added := tiles["Recently added"]
	if want := "1\\. **New \\*One\\*** (2024)\n2\\. **Old One** (1999)"; added.Description != want {
		t.Errorf("recently added = %q, want %q", added.Description, want)
	}
	if added.Thumbnail != "" || added.Image != "attachment://added.jpg" {
		t.Errorf("thumbnail %q image %q", added.Thumbnail, added.Image)
	}

	w := tiles["Recently watched"].Description
	wantW := fmt.Sprintf("**Old One** · <t:%d:R>\n**New \\*One\\*** (2024) · <t:%d:R>\n**Third**",
		r.clock.Now().Add(-time.Hour).Unix(), r.clock.Now().Add(-2*time.Hour).Unix())
	if w != wantW {
		t.Errorf("recently watched = %q, want %q", w, wantW)
	}

	// Alice (two accounts, 5 plays) and Carol opted in; Bob did not; Ghost has
	// no HolyArrs user; Nolink has no link; Idle has no plays.
	top := tiles["Top watchers"].Description
	if want := "1\\. <@1001> · 5 plays\n2\\. <@1003> · 1 play\nand 3 others, 31 plays"; top != want {
		t.Errorf("top watchers = %q, want %q", top, want)
	}
	for _, e := range []core.Embed{tiles["Recently watched"], tiles["Top watchers"]} {
		for _, name := range []string{"Alice", "alice", "Bob", "Ghost", "Nolink", "Carol", "Idle", "1002"} {
			if strings.Contains(e.Description, name) {
				t.Errorf("%s shows %q: %q", e.Title, name, e.Description)
			}
		}
	}

	up := tiles["Coming up"].Description
	wantUp := fmt.Sprintf("1\\. **Soon** · <t:%d:R>\n2\\. **Later** · S01E02 · <t:%d:R>",
		r.clock.Now().Add(24*time.Hour).Unix(), r.clock.Now().Add(72*time.Hour).Unix())
	if up != wantUp {
		t.Errorf("upcoming = %q, want %q", up, wantUp)
	}
	// Only the two poster tiles carry a picture.
	for _, m := range sends {
		e := m.Msg.Embeds[0]
		grid := e.Title == "Recently added" || e.Title == "Coming up"
		if (len(m.Msg.Files) == 1) != grid || (e.Image != "") != grid {
			t.Errorf("%s: files=%d image=%q", e.Title, len(m.Msg.Files), e.Image)
		}
	}
	for _, e := range tiles {
		if !e.Time.Equal(r.clock.Now()) || e.Footer != "Updated" {
			t.Errorf("tile %q: time %v footer %q", e.Title, e.Time, e.Footer)
		}
	}
}

func TestHashSkipsEdits(t *testing.T) {
	r := newRig(onCfg())
	seed(r)
	f := New(r.deps)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	r.clock.Add(10 * time.Minute) // only the clock moves
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sends, edits, _, _, _ := r.dc.snap()
	if len(sends) != 4 || len(edits) != 0 {
		t.Fatalf("unchanged content must not edit: sends=%d edits=%d", len(sends), len(edits))
	}

	r.ha.mu.Lock()
	r.ha.recent = append(r.ha.recent, core.Title{ID: 8, Name: "Brand New", AddedAt: r.clock.Now()})
	r.ha.mu.Unlock()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sends, edits, _, _, _ = r.dc.snap()
	if len(sends) != 4 || len(edits) != 1 || edits[0].ID != sends[0].ID {
		t.Fatalf("want exactly one edit of the added tile: sends=%d edits=%+v", len(sends), edits)
	}
	if !edits[0].Msg.Embeds[0].Time.Equal(r.clock.Now()) {
		t.Errorf("edited tile should carry the new time")
	}

	// A new process with the same store must not re-send or edit.
	f2 := New(r.deps)
	if err := f2.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sends, edits, _, _, _ = r.dc.snap()
	if len(sends) != 4 || len(edits) != 1 {
		t.Fatalf("after restart: sends=%d edits=%d", len(sends), len(edits))
	}
}

// ---- poster-grid tiles ----

func posterJPEG(t testing.TB, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 60, 90))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func artRef(id int) string { return fmt.Sprintf("/api/v1/artwork/p%d.jpg", id) }

// gridRig has n recently added titles, newest first, each with a stored
// poster, and no other tile content.
func gridRig(t testing.TB, n int) *rig {
	r := newRig(onCfg())
	r.ha.art = map[string][]byte{}
	r.ha.artErr = map[string]error{}
	for i := 1; i <= n; i++ {
		r.ha.recent = append(r.ha.recent, core.Title{ID: core.TitleID(i), Name: fmt.Sprintf("Title %d", i), Year: 2000 + i,
			AddedAt: r.clock.Now().Add(-time.Duration(i) * time.Hour), Artwork: artRef(i)})
		r.ha.art[artRef(i)] = posterJPEG(t, color.RGBA{0xc0, 0x30, 0x30, 0xff})
	}
	return r
}

// addedTile is the newest version of the Recently added tile.
func addedTile(t testing.TB, r *rig) sentMsg {
	t.Helper()
	sends, edits, _, _, _ := r.dc.snap()
	var out *sentMsg
	for _, list := range [][]sentMsg{sends, edits} {
		for i := range list {
			if list[i].Msg.Embeds[0].Title == "Recently added" {
				out = &list[i]
			}
		}
	}
	if out == nil {
		t.Fatal("no Recently added tile")
	}
	return *out
}

func gridImage(t testing.TB, m sentMsg) image.Image {
	t.Helper()
	if len(m.Msg.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(m.Msg.Files))
	}
	f := m.Msg.Files[0]
	if f.Name != "added.jpg" || f.ContentType != "image/jpeg" || m.Msg.Embeds[0].Image != "attachment://added.jpg" {
		t.Fatalf("file %q %q, embed image %q", f.Name, f.ContentType, m.Msg.Embeds[0].Image)
	}
	if len(f.Data) > 1<<20 {
		t.Errorf("upload is %d bytes", len(f.Data))
	}
	img, err := jpeg.Decode(bytes.NewReader(f.Data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != postergrid.Width || b.Dy() != postergrid.Height {
		t.Fatalf("picture is %v", b)
	}
	return img
}

// cellIsPoster reports whether grid cell i (0 based) shows the red test poster.
func cellIsPoster(img image.Image, i int) bool {
	x := postergrid.Width/6 + (i%3)*postergrid.Width/3
	y := postergrid.Height/6 + (i/3)*postergrid.Height/3
	r, g, b, _ := img.At(x, y).RGBA()
	return r>>8 > 0x90 && g>>8 < 0x60 && b>>8 < 0x60
}

func TestGridTile(t *testing.T) {
	for _, n := range []int{1, 9, 12} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := gridRig(t, n)
			if err := New(r.deps).Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			m := addedTile(t, r)
			img := gridImage(t, m)
			shown := min(n, gridCells)
			for i := 0; i < gridCells; i++ {
				if cellIsPoster(img, i) != (i < shown) {
					t.Errorf("cell %d: poster = %v", i, cellIsPoster(img, i))
				}
			}
			// The numbered list matches the grid: newest first, at most nine.
			lines := strings.Split(m.Msg.Embeds[0].Description, "\n")
			if len(lines) != shown {
				t.Fatalf("list has %d lines, want %d: %q", len(lines), shown, lines)
			}
			for i, l := range lines {
				if want := fmt.Sprintf("%d\\. **Title %d** (%d)", i+1, i+1, 2001+i); l != want {
					t.Errorf("line %d = %q, want %q", i, l, want)
				}
			}
			if got := r.ha.count("artwork"); got != shown {
				t.Errorf("%d poster reads, want %d", got, shown)
			}
		})
	}
}

func TestGridTileWithNothing(t *testing.T) {
	r := gridRig(t, 0)
	if err := New(r.deps).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	sends, _, _, _, _ := r.dc.snap()
	for _, m := range sends {
		e := m.Msg.Embeds[0]
		if e.Title != "Recently added" && e.Title != "Coming up" {
			continue
		}
		if len(m.Msg.Files) != 0 || e.Image != "" || e.Description != "Nothing here yet." {
			t.Errorf("%s: files=%d image=%q description=%q", e.Title, len(m.Msg.Files), e.Image, e.Description)
		}
	}
	if r.ha.count("artwork") != 0 {
		t.Error("posters were read for an empty tile")
	}
}

func TestGridMissingPosters(t *testing.T) {
	r := gridRig(t, 5)
	r.ha.recent[1].Artwork = ""                                   // no stored poster
	delete(r.ha.art, artRef(3))                                   // stored, but gone
	r.ha.art[artRef(4)] = []byte("not a picture")                 // unusable
	r.ha.art[artRef(5)] = make([]byte, maxPosterBytes+1)          // over the size cap
	r.ha.recent[4].PosterURL = "https://image.tmdb.org/t/p/x.jpg" // never fetched: not a host we may reach
	f := New(r.deps)
	if err := f.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	img := gridImage(t, addedTile(t, r))
	for i := 0; i < gridCells; i++ {
		if cellIsPoster(img, i) != (i == 0) {
			t.Errorf("cell %d: poster = %v", i, cellIsPoster(img, i))
		}
	}
	if got := r.ha.count("artwork"); got != 4 {
		t.Errorf("%d poster reads, want 4 (none for the title without one)", got)
	}
	// None of these will get better by asking again.
	if err := f.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, edits, _, _, _ := r.dc.snap(); len(edits) != 0 || r.ha.count("artwork") != 4 {
		t.Errorf("edits=%d reads=%d after a second refresh", len(edits), r.ha.count("artwork"))
	}
}

func TestGridSharedPosterIsReadOnce(t *testing.T) {
	r := gridRig(t, 0)
	r.ha.art["/api/v1/artwork/show.jpg"] = posterJPEG(t, color.RGBA{0xc0, 0x30, 0x30, 0xff})
	for i := 1; i <= 12; i++ { // twelve episodes of one show
		r.ha.upcoming = append(r.ha.upcoming, core.CalendarItem{Label: fmt.Sprintf("S01E%02d", i), Date: r.clock.Now().Add(time.Duration(i) * 24 * time.Hour),
			Title: core.Title{ID: 4, Name: "The Show", Year: 2025, Artwork: "/api/v1/artwork/show.jpg"}})
	}
	if err := New(r.deps).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.ha.count("artwork"); got != 1 {
		t.Errorf("%d poster reads, want 1", got)
	}
	sends, _, _, _, _ := r.dc.snap()
	up := byTitle(sends)["Coming up"]
	lines := strings.Split(up.Description, "\n")
	if len(lines) != gridCells || up.Image != "attachment://upcoming.jpg" ||
		lines[8] != fmt.Sprintf("9\\. **The Show** (2025) · S01E09 · <t:%d:R>", r.clock.Now().Add(9*24*time.Hour).Unix()) {
		t.Errorf("coming up: image %q, lines %q", up.Image, lines)
	}
}

func TestGridSkipsWhenUnchanged(t *testing.T) {
	r := gridRig(t, 9)
	f := New(r.deps)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	reads := r.ha.count("artwork")
	r.clock.Add(10 * time.Minute)
	for _, ft := range []*Feature{f, New(r.deps)} { // the same process, then a restart
		if err := ft.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sends, edits, _, _, _ := r.dc.snap()
	if len(sends) != 4 || len(edits) != 0 || r.ha.count("artwork") != reads {
		t.Fatalf("unchanged: sends=%d edits=%d poster reads %d -> %d", len(sends), len(edits), reads, r.ha.count("artwork"))
	}

	// A new poster for the same title is a change, with the text the same.
	r.ha.mu.Lock()
	r.ha.recent[2].Artwork = "/api/v1/artwork/fresh.jpg"
	r.ha.art["/api/v1/artwork/fresh.jpg"] = posterJPEG(t, color.RGBA{0xc0, 0x30, 0x30, 0xff})
	r.ha.mu.Unlock()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sends, edits, _, _, _ = r.dc.snap()
	if len(sends) != 4 || len(edits) != 1 || edits[0].ID != sends[0].ID {
		t.Fatalf("want one edit of the added tile: sends=%d edits=%d", len(sends), len(edits))
	}
	// The edit carries exactly one picture; the adapter drops the old one.
	if len(edits[0].Msg.Files) != 1 || edits[0].Msg.Files[0].Name != "added.jpg" {
		t.Errorf("edit files = %+v", len(edits[0].Msg.Files))
	}
}

func TestGridRetriesFailedPosterReads(t *testing.T) {
	r := gridRig(t, 3)
	r.ha.artErr[artRef(2)] = errors.New("holyarrs is busy")
	f := New(r.deps)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	img := gridImage(t, addedTile(t, r))
	if !cellIsPoster(img, 0) || cellIsPoster(img, 1) || !cellIsPoster(img, 2) {
		t.Fatal("want the failed poster as a title cell and the others drawn")
	}
	// It is tried again on later refreshes, a few times and no more.
	for i := 0; i < posterRetries+3; i++ {
		if err := f.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_, edits, _, _, _ := r.dc.snap()
	if len(edits) != posterRetries-1 {
		t.Fatalf("%d edits while the read keeps failing, want %d", len(edits), posterRetries-1)
	}

	// When the read works on a retry, the tile is redrawn once and left alone.
	r2 := gridRig(t, 3)
	r2.ha.artErr[artRef(2)] = errors.New("holyarrs is busy")
	f2 := New(r2.deps)
	if err := f2.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	r2.ha.mu.Lock()
	delete(r2.ha.artErr, artRef(2))
	r2.ha.mu.Unlock()
	for i := 0; i < 3; i++ {
		if err := f2.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, edits, _, _, _ := r2.dc.snap(); len(edits) != 1 || !cellIsPoster(gridImage(t, edits[0]), 1) {
		t.Fatalf("%d edits after the read recovered, want 1 with the poster", len(edits))
	}
}

func TestGridWithoutArtworkPermission(t *testing.T) {
	r := gridRig(t, 2)
	for i := 1; i <= 2; i++ {
		r.ha.artErr[artRef(i)] = fmt.Errorf("no permission: %w", core.ErrForbidden)
	}
	f := New(r.deps)
	for i := 0; i < 3; i++ {
		if err := f.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Title cells, and no retry: only the owner can change this.
	img := gridImage(t, addedTile(t, r))
	if cellIsPoster(img, 0) || cellIsPoster(img, 1) {
		t.Error("posters drawn without the permission")
	}
	if _, edits, _, _, _ := r.dc.snap(); len(edits) != 0 || r.ha.count("artwork") != 2 {
		t.Errorf("edits=%d reads=%d", len(edits), r.ha.count("artwork"))
	}
}

func TestGridReadsAreBounded(t *testing.T) {
	r := gridRig(t, 9)
	var mu sync.Mutex
	now, peak := 0, 0
	r.deps.HA = &slowArt{fakeHA: r.ha, enter: func() {
		mu.Lock()
		now++
		peak = max(peak, now)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		now--
		mu.Unlock()
	}}
	if err := New(r.deps).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if peak < 1 || peak > posterFetchers {
		t.Errorf("%d poster reads at once, want at most %d", peak, posterFetchers)
	}
}

type slowArt struct {
	*fakeHA
	enter func()
}

func (s *slowArt) Artwork(ctx context.Context, ref string) ([]byte, string, error) {
	s.enter()
	return s.fakeHA.Artwork(ctx, ref)
}

func TestRecreateWhenMessageDeleted(t *testing.T) {
	r := newRig(onCfg())
	seed(r)
	f := New(r.deps)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sends, _, _, _, _ := r.dc.snap()
	gone := sends[3].ID // the Coming up tile
	r.dc.mu.Lock()
	r.dc.editErr[gone] = fmt.Errorf("wrapped: %w", core.ErrNotFound)
	r.dc.mu.Unlock()
	r.ha.mu.Lock()
	r.ha.upcoming = r.ha.upcoming[:1]
	r.ha.mu.Unlock()

	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sends, edits, _, pins, _ := r.dc.snap()
	if len(sends) != 5 || len(pins) != 5 || len(edits) != 0 {
		t.Fatalf("sends=%d pins=%d edits=%d", len(sends), len(pins), len(edits))
	}
	if pins[4] != sends[4].ID || sends[4].Msg.Embeds[0].Title != "Coming up" {
		t.Fatalf("new tile not pinned: %+v", sends[4])
	}
	var doc tilesDoc
	_ = r.store.Load(storeDoc, &doc)
	if doc.Tiles[tileUpcoming].MessageID != sends[4].ID {
		t.Fatalf("state not updated: %+v", doc.Tiles)
	}
}

func TestOneTileFailingDoesNotStopOthers(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(r *rig)
		want    []string
	}{
		{"upcoming", func(r *rig) { r.ha.upcomingErr = errors.New("boom") }, []string{"Recently added", "Recently watched", "Top watchers"}},
		{"added", func(r *rig) { r.ha.recentErr = errors.New("boom") }, []string{"Recently watched", "Top watchers", "Coming up"}},
		{"watched", func(r *rig) { r.ha.watchedErr = errors.New("boom") }, []string{"Recently added", "Top watchers", "Coming up"}},
		{"people", func(r *rig) { r.ha.peopleErr = errors.New("boom") }, []string{"Recently added", "Recently watched", "Coming up"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(onCfg())
			seed(r)
			c.breakIt(r)
			f := New(r.deps)
			if err := f.Refresh(context.Background()); err == nil {
				t.Fatal("want an error reported")
			}
			sends, _, _, _, _ := r.dc.snap()
			got := byTitle(sends)
			if len(got) != len(c.want) {
				t.Fatalf("got %d tiles, want %v", len(got), c.want)
			}
			for _, w := range c.want {
				if _, ok := got[w]; !ok {
					t.Errorf("missing tile %q", w)
				}
			}
			if r.ha.count("watched") != 1 || r.ha.count("people") != 1 {
				t.Errorf("fetches: watched %d, people %d, want 1 each", r.ha.count("watched"), r.ha.count("people"))
			}
		})
	}
}

func TestLimits(t *testing.T) {
	r := newRig(onCfg())
	long := strings.Repeat("*_x", 400)
	for i := 0; i < 30; i++ {
		r.ha.recent = append(r.ha.recent, core.Title{ID: core.TitleID(i + 1), Name: long, Year: 2000 + i})
		r.ha.watched = append(r.ha.watched, core.WatchedTitle{
			Title: core.Title{ID: core.TitleID(i + 1), Name: long}, LastWatched: r.clock.Now().Add(-time.Duration(i) * time.Minute),
		})
		u := core.UserID(fmt.Sprintf("u%d", i))
		r.ha.people = append(r.ha.people, core.PersonWatch{UserID: u, UserName: long, Plays: i + 1})
		_ = r.links.Put(core.Link{DiscordID: fmt.Sprintf("%d", 5000+i), UserID: u, ShowWatching: true})
	}
	if err := New(r.deps).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	sends, _, _, _, _ := r.dc.snap()
	for _, s := range sends {
		e := s.Msg.Embeds[0]
		if n := utf8.RuneCountInString(e.Description); n > maxEmbedDesc {
			t.Errorf("%s: description %d runes", e.Title, n)
		}
		rows := tileRows
		if e.Title == "Top watchers" {
			rows++ // the closing "and N others" line
		}
		if n := strings.Count(e.Description, "\n") + 1; n > rows {
			t.Errorf("%s: %d rows", e.Title, n)
		}
		if !strings.Contains(e.Description, "…") && strings.HasPrefix(e.Title, "Recently") {
			t.Errorf("%s: long names should be cut with an ellipsis", e.Title)
		}
		if e.Title == "Top watchers" {
			// Ranked by plays: the ten busiest are named, the other twenty folded.
			if !strings.HasPrefix(e.Description, "1\\. <@5029> · 30 plays\n2\\. <@5028> · 29 plays\n") ||
				!strings.HasSuffix(e.Description, "\n10\\. <@5020> · 21 plays\nand 20 others, 210 plays") {
				t.Errorf("top watchers = %q", e.Description)
			}
		}
	}
}

func TestWatchTilesWhenEmptyOrPrivate(t *testing.T) {
	links := &fakeLinks{}
	_ = links.Put(core.Link{DiscordID: "1001", UserID: "u1", ShowWatching: true})
	_ = links.Put(core.Link{DiscordID: "not-an-id", UserID: "u5", ShowWatching: true})
	cases := []struct {
		name   string
		people []core.PersonWatch
		want   string
	}{
		{"nobody", nil, "Nothing yet"},
		{"no plays", []core.PersonWatch{{UserID: "u1"}}, "Nothing yet"},
		{"one hidden", []core.PersonWatch{{UserID: "u2", UserName: "Bob", Plays: 1}}, "1 person"},
		{"all hidden", []core.PersonWatch{{UserName: "Ghost", Plays: 4}, {UserID: "u2", UserName: "Bob", Plays: 1}}, "2 people"},
		{"one shown, one hidden", []core.PersonWatch{{UserID: "u2", UserName: "Bob", Plays: 7}, {UserID: "u1", UserName: "Alice", Plays: 2}},
			"1\\. <@1001> · 2 plays\nand 1 other"},
		{"one shown, two hidden", []core.PersonWatch{{UserID: "u2", UserName: "Bob", Plays: 7}, {UserName: "Ghost", Plays: 4},
			{UserID: "u1", UserName: "Alice", Plays: 2}}, "1\\. <@1001> · 2 plays\nand 2 others"},
		{"one shown, three hidden", []core.PersonWatch{{UserID: "u2", UserName: "Bob", Plays: 7}, {UserName: "Ghost", Plays: 4},
			{UserName: "Ghost2", Plays: 1}, {UserID: "u1", UserName: "Alice", Plays: 2}}, "1\\. <@1001> · 2 plays\nand 3 others, 12 plays"},
		{"three hidden, nobody shown", []core.PersonWatch{{UserID: "u2", Plays: 7}, {UserName: "Ghost", Plays: 4}, {UserName: "Ghost2", Plays: 1}},
			"3 people, 12 plays"},
		{"only shown", []core.PersonWatch{{UserID: "u1", UserName: "Alice", Plays: 2}}, "1\\. <@1001> · 2 plays"},
		{"odd discord id is never a mention", []core.PersonWatch{{UserID: "u5", UserName: "Eve", Plays: 2}}, "1 person"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderTop(c.people, links).Description; got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	if got := renderTop([]core.PersonWatch{{UserID: "u1", Plays: 2}}, nil).Description; got != "1 person" {
		t.Errorf("nil links: %q", got)
	}
	if got := renderWatched(nil); got.Title != "Recently watched" || got.Description != "Nothing yet" {
		t.Errorf("empty recently watched = %+v", got)
	}
}

func TestTextHelpers(t *testing.T) {
	trunc := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"}, {"hello", 5, "hello"}, {"hello", 4, "hel…"},
		{"héllo wörld", 6, "héllo…"}, {"x", 0, ""}, {`ab\cd`, 4, "ab…"},
	}
	for _, c := range trunc {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q,%d)=%q want %q", c.in, c.n, got, c.want)
		}
	}
	esc := []struct{ in, want string }{
		{"a*b_c", `a\*b\_c`}, {"@everyone", "@​everyone"}, {"<@123>", `\<@` + "​" + `123\>`},
		{"line\none", "line one"}, {"[x](http://y)", `\[x\]\(http://y\)`},
	}
	for _, c := range esc {
		if got := escapeMD(c.in); got != c.want {
			t.Errorf("escapeMD(%q)=%q want %q", c.in, got, c.want)
		}
	}
	e := core.Embed{Title: strings.Repeat("t", 300), Description: strings.Repeat("d", 5000), Footer: "f"}
	for i := 0; i < 30; i++ {
		e.Fields = append(e.Fields, core.Field{Name: strings.Repeat("n", 300), Value: strings.Repeat("v", 2000)})
	}
	c := clampEmbed(e)
	total := utf8.RuneCountInString(c.Title) + utf8.RuneCountInString(c.Description) + utf8.RuneCountInString(c.Footer)
	for _, f := range c.Fields {
		total += utf8.RuneCountInString(f.Name) + utf8.RuneCountInString(f.Value)
		if utf8.RuneCountInString(f.Value) > maxFieldValue || utf8.RuneCountInString(f.Name) > maxFieldName {
			t.Errorf("field over limit")
		}
	}
	if utf8.RuneCountInString(c.Title) != maxEmbedTitle || utf8.RuneCountInString(c.Description) > maxEmbedDesc ||
		len(c.Fields) > maxFields || total > maxEmbedTotal {
		t.Errorf("clamp: title=%d desc=%d fields=%d total=%d", utf8.RuneCountInString(c.Title),
			utf8.RuneCountInString(c.Description), len(c.Fields), total)
	}
	lines := []string{strings.Repeat("a", 10), strings.Repeat("b", 10), strings.Repeat("c", 10)}
	if got := joinLines(lines, 25); got != lines[0]+"\n"+lines[1]+"\n…" {
		t.Errorf("joinLines = %q", got)
	}
	if got := joinLines(lines, 32); got != strings.Join(lines, "\n") {
		t.Errorf("joinLines full = %q", got)
	}
}

func TestRunDisabledWaits(t *testing.T) {
	for _, cfg := range []core.Config{
		{EnableDashboard: false, DashboardChannel: "chan"},
		{EnableDashboard: true, DashboardChannel: ""},
	} {
		r := newRig(cfg)
		seed(r)
		f := New(r.deps)
		if len(f.Commands()) != 0 {
			t.Errorf("no commands when off")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- f.Run(ctx) }()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if sends, _, _, _, _ := r.dc.snap(); len(sends) != 0 {
			t.Errorf("disabled dashboard sent %d messages", len(sends))
		}
	}
}

func TestRunDebouncedEventRefresh(t *testing.T) {
	r := newRig(onCfg())
	seed(r)
	f := New(r.deps)
	f.debounce = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	if !waitFor(func() bool { s, _, _, _, _ := r.dc.snap(); return len(s) == 4 }) {
		t.Fatal("initial tiles not sent")
	}
	r.ha.mu.Lock()
	r.ha.recent = append(r.ha.recent, core.Title{ID: 9, Name: "Event Title", AddedAt: r.clock.Now()})
	r.ha.mu.Unlock()
	r.ev.ch <- core.Event{Type: core.EventOther} // ignored
	for i := 0; i < 5; i++ {
		r.ev.ch <- core.Event{Type: core.EventTitleAdded, TitleID: 9}
	}
	if !waitFor(func() bool { _, e, _, _, _ := r.dc.snap(); return len(e) == 1 }) {
		t.Fatal("event did not cause a refresh")
	}
	time.Sleep(15 * time.Millisecond)
	if _, e, _, _, _ := r.dc.snap(); len(e) != 1 {
		t.Fatalf("burst of events caused %d edits, want 1", len(e))
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRefreshCommand(t *testing.T) {
	r := newRig(onCfg())
	seed(r)
	f := New(r.deps)
	cmds := f.Commands()
	if len(cmds) != 1 || cmds[0].Name != "dashboard" || !cmds[0].AdminOnly || len(cmds[0].Sub) != 1 ||
		cmds[0].Sub[0].Name != "refresh" || !cmds[0].Sub[0].AdminOnly {
		t.Fatalf("commands = %+v", cmds)
	}
	h := cmds[0].Sub[0].Handle
	if err := h(context.Background(), &core.Interaction{UserID: "x", IsAdmin: false}); err != nil {
		t.Fatal(err)
	}
	if s, _, _, _, _ := r.dc.snap(); len(s) != 0 {
		t.Fatal("non-admin must not refresh")
	}
	if rp := r.dc.lastReply(); rp.Kind != "respond" || !rp.Ephemeral {
		t.Fatalf("reply = %+v", rp)
	}
	if err := h(context.Background(), &core.Interaction{UserID: "x", IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if s, _, _, _, _ := r.dc.snap(); len(s) != 4 {
		t.Fatalf("admin refresh sent %d", len(s))
	}
	if rp := r.dc.lastReply(); rp.Kind != "followup" || !rp.Ephemeral {
		t.Fatalf("reply = %+v", rp)
	}
}

// The dashboard subscribes when it is built, so events published before Run
// starts are waiting for it.
func TestSubscribesInNew(t *testing.T) {
	r := newRig(onCfg())
	seed(r)
	ev := &countingEvents{ch: make(chan core.Event, 8)}
	r.deps.Events = ev
	f := New(r.deps)
	if got := ev.names(); len(got) != 1 || got[0] != "dashboard" {
		t.Fatalf("subscriptions after New = %v", got)
	}
	f.debounce = 5 * time.Millisecond
	r.ha.mu.Lock()
	r.ha.recent = append(r.ha.recent, core.Title{ID: 9, Name: "Early Title", AddedAt: r.clock.Now()})
	r.ha.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	if !waitFor(func() bool { s, _, _, _, _ := r.dc.snap(); return len(s) == 4 }) {
		t.Fatal("initial tiles not sent")
	}
	if got := ev.names(); len(got) != 1 {
		t.Fatalf("Run subscribed again: %v", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A disabled dashboard must not hold a channel nobody reads.
	ev2 := &countingEvents{ch: make(chan core.Event, 1)}
	off := newRig(core.Config{DashboardChannel: "chan"})
	off.deps.Events = ev2
	New(off.deps)
	if got := ev2.names(); len(got) != 0 {
		t.Fatalf("disabled dashboard subscribed: %v", got)
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
