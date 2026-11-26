package postergrid

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"runtime"
	"strings"
	"testing"
)

// minimalWebP is a 1x1 lossless WebP.
const minimalWebP = "RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00\x2f\x00\x00\x00\x10\x07\x10\x11\x11\x88\x88\xfe\x07\x00"

func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Set(i%w, i/w, c)
	}
	return img
}

func jpegOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var (
	red  = color.RGBA{0xd0, 0x20, 0x20, 0xff}
	blue = color.RGBA{0x20, 0x30, 0xd0, 0xff}
)

func near(a color.Color, b color.RGBA, tol int) bool {
	r, g, bl, _ := a.RGBA()
	d := func(x uint32, y uint8) bool {
		v := int(x>>8) - int(y)
		return v >= -tol && v <= tol
	}
	return d(r, b.R) && d(g, b.G) && d(bl, b.B)
}

// centre is the pixel in the middle of cell i of a composed grid.
func centre(img image.Image, i int) color.Color {
	r := cellRect(i)
	return img.At(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
}

// hasText reports whether cell i holds text-coloured pixels below its badge.
func hasText(img image.Image, i int) bool {
	r := cellRect(i)
	for y := r.Min.Y + 60; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if near(img.At(x, y), colText, 40) {
				return true
			}
		}
	}
	return false
}

func compose(t *testing.T, cells []Cell) (image.Image, int, int) {
	t.Helper()
	jpg, drawn, err := Compose(cells)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(jpg))
	if err != nil || format != "jpeg" {
		t.Fatalf("output: %s %v", format, err)
	}
	if b := img.Bounds(); b.Dx() != Width || b.Dy() != Height {
		t.Fatalf("size %v, want %dx%d", b, Width, Height)
	}
	return img, drawn, len(jpg)
}

func TestSize(t *testing.T) {
	if Width > 900 || Height > 1350 || cellW*3 != cellH*2 {
		t.Fatalf("grid %dx%d, cell %dx%d", Width, Height, cellW, cellH)
	}
}

func TestComposeCounts(t *testing.T) {
	poster := jpegOf(t, solid(400, 600, red))
	for _, n := range []int{0, 1, 9, 12} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			cells := make([]Cell, n)
			for i := range cells {
				cells[i] = Cell{Title: fmt.Sprintf("Title %d", i+1), Poster: poster}
			}
			img, drawn, size := compose(t, cells)
			want := min(n, Cells)
			if drawn != want {
				t.Errorf("drawn = %d, want %d", drawn, want)
			}
			for i := 0; i < Cells; i++ {
				got := centre(img, i)
				if i < want && !near(got, red, 30) {
					t.Errorf("cell %d = %v, want the poster", i, got)
				}
				if i >= want && (!near(got, colEmpty, 12) || hasText(img, i)) {
					t.Errorf("cell %d = %v, want an empty dark cell", i, got)
				}
			}
			if !near(img.At(1, 1), colBack, 12) {
				t.Errorf("gap = %v", img.At(1, 1))
			}
			if size > 1<<20 {
				t.Errorf("%d bytes", size)
			}
		})
	}
}

func TestMissingAndBadPosters(t *testing.T) {
	huge := pngOf(t, image.NewGray(image.Rect(0, 0, 4001, 3000))) // over MaxPixels
	cells := []Cell{
		{Title: "Has a poster", Poster: jpegOf(t, solid(300, 450, red))},
		{Title: "No poster at all"},
		{Title: "Broken bytes", Poster: []byte("not a picture")},
		{Title: "A PNG", Poster: pngOf(t, solid(200, 300, blue))},
		{Title: "Far too large", Poster: huge},
		{Title: "", Poster: nil},
		{Title: "A WebP", Poster: []byte(minimalWebP)},
	}
	img, drawn, _ := compose(t, cells)
	if drawn != 3 {
		t.Errorf("drawn = %d, want 3", drawn)
	}
	if !near(centre(img, 0), red, 30) || !near(centre(img, 3), blue, 30) {
		t.Errorf("posters: %v %v", centre(img, 0), centre(img, 3))
	}
	for _, i := range []int{1, 2, 4} {
		if !hasText(img, i) {
			t.Errorf("cell %d has no title drawn", i)
		}
		r := cellRect(i)
		if !near(img.At(r.Max.X-3, r.Max.Y-3), colEmpty, 12) {
			t.Errorf("cell %d is not dark", i)
		}
	}
	if hasText(img, 5) || hasText(img, 7) {
		t.Error("a cell with no title, or past the end, has text")
	}
}

func TestCoverKeepsShape(t *testing.T) {
	// A wide picture, red in the middle third and blue at the sides: the
	// cell shows the middle only.
	wide := solid(900, 300, blue)
	for y := 0; y < 300; y++ {
		for x := 300; x < 600; x++ {
			wide.Set(x, y, red)
		}
	}
	img, _, _ := compose(t, []Cell{{Poster: pngOf(t, wide)}})
	r := cellRect(0)
	for _, x := range []int{r.Min.X + 60, r.Min.X + r.Dx()/2, r.Max.X - 20} {
		if got := img.At(x, r.Max.Y-20); !near(got, red, 30) {
			t.Errorf("x=%d: %v, want the middle of the picture", x, got)
		}
	}
}

func TestDecodeRefuses(t *testing.T) {
	if _, err := Decode(pngOf(t, image.NewGray(image.Rect(0, 0, maxSide+1, 2)))); err == nil {
		t.Error("a picture wider than maxSide was decoded")
	}
	if _, err := Decode(nil); err == nil {
		t.Error("no bytes were decoded")
	}
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	if _, err := Decode(gif); err == nil {
		t.Error("a GIF was decoded")
	}
	if img, err := Decode([]byte(minimalWebP)); err != nil || img.Bounds().Dx() != 1 {
		t.Errorf("webp: %v", err)
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		in       string
		per, max int
		want     string
	}{
		{"The Lord of the Rings", 10, 5, "The Lord|of the|Rings"},
		{"  spaced   out  ", 10, 5, "spaced out"},
		{"Supercalifragilistic", 8, 5, "Supercal|ifragili|stic"},
		{"one two three four five six", 5, 2, "one|tw..."},
		{"abcdefghij klm", 5, 2, "abcde|fg..."},
		{"", 10, 5, ""},
		{"anything", 3, 5, ""},
		{"anything", 10, 0, ""},
	}
	for _, c := range cases {
		got := wrap(c.in, c.per, c.max)
		if s := strings.Join(got, "|"); s != c.want {
			t.Errorf("wrap(%q, %d, %d) = %q, want %q", c.in, c.per, c.max, s, c.want)
		}
		for _, l := range got {
			if len([]rune(l)) > c.per {
				t.Errorf("line %q is longer than %d", l, c.per)
			}
		}
	}
}

func TestLongTitleStaysInItsCell(t *testing.T) {
	img, _, _ := compose(t, []Cell{{Title: strings.Repeat("Wordy title ", 80)}, {}})
	if !hasText(img, 0) {
		t.Fatal("no title drawn")
	}
	if hasText(img, 1) || hasText(img, 3) {
		t.Error("the title ran into another cell")
	}
}

// maxPoster is a picture of exactly the largest size Decode takes, in the
// shape of a poster: 1154x1733 is 1,999,882 pixels.
func maxPoster() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1154, 1733))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 31)
	}
	return img
}

// Nine posters of the largest size that is decoded must fit the plugin's
// 512 MiB. TotalAlloc is everything Compose allocated, kept or not, so it is
// an upper bound on what was live at any moment and does not depend on when
// the collector ran: under the limits below the nine fit even if nothing were
// freed in between. Most of it is the scaler's buffer (cell width x source
// height x 32 bytes, 16 MiB here); a real small poster needs about 2 MiB.
// Measured: 172 MiB (JPEG) and 215 MiB (PNG) in all; peak RSS about 90 MiB,
// and about 20 MiB for nine small posters.
func TestComposeNineLargestPostersStaysSmall(t *testing.T) {
	if int64(1154)*1733 > MaxPixels || int64(1155)*1734 <= MaxPixels {
		t.Fatalf("the test picture is not the largest one for MaxPixels = %d", MaxPixels)
	}
	for _, tc := range []struct {
		name  string
		data  []byte
		limit uint64 // bytes allocated by one Compose
	}{
		{"jpeg", jpegOf(t, maxPoster()), 256 << 20},
		{"png", pngOf(t, maxPoster()), 320 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cells := make([]Cell, Cells)
			for i := range cells {
				cells[i] = Cell{Title: "T", Poster: tc.data}
			}
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			jpg, drawn, err := Compose(cells)
			runtime.ReadMemStats(&after)
			if err != nil || drawn != Cells {
				t.Fatalf("drawn %d, err %v", drawn, err)
			}
			got := after.TotalAlloc - before.TotalAlloc
			t.Logf("nine %s posters of %d pixels: %.1f MiB allocated in all, %d KiB out", tc.name, 1154*1733, float64(got)/(1<<20), len(jpg)>>10)
			if got > tc.limit {
				t.Errorf("Compose allocated %d MiB, want at most %d MiB", got>>20, tc.limit>>20)
			}
		})
	}
}
