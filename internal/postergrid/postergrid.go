// Package postergrid draws a 3x3 grid of posters as one JPEG. It is pure Go:
// it knows nothing about Discord or HolyArrs and only works on bytes.
package postergrid

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // stored posters can be PNG
	"strconv"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp" // HolyArrs stores WebP too
)

const (
	// Cols and Rows are the grid; Cells is how many posters fit.
	Cols  = 3
	Rows  = 3
	Cells = Cols * Rows

	gap   = 6   // around and between cells
	cellW = 292 // 2:3
	cellH = 438

	// Width and Height of the finished image.
	Width  = Cols*cellW + (Cols+1)*gap
	Height = Rows*cellH + (Rows+1)*gap

	quality = 85

	// MaxPixels is the largest poster that is decoded. The grid is drawn from
	// HolyArrs's small posters (250 px a side, about 0.04 million pixels), so
	// 2 million is generous. Decoding one of that size takes 3 MB (JPEG) to
	// 16 MB (16-bit PNG) for a moment; a cell is only 292x438.
	MaxPixels = 2_000_000
	maxSide   = 8000

	textScale = 2 // the 7x13 font drawn at 14x26
	textPad   = 14
)

var (
	colBack  = color.RGBA{0x1e, 0x1f, 0x22, 0xff} // behind the cells
	colEmpty = color.RGBA{0x2b, 0x2d, 0x31, 0xff} // a cell with no poster
	colText  = color.RGBA{0xdb, 0xde, 0xe1, 0xff}
	colBadge = color.RGBA{0x11, 0x12, 0x14, 0xff}
)

// Cell is one place in the grid. Poster holds the picture bytes (JPEG, PNG
// or WebP); when it is empty or cannot be used, Title is drawn instead.
type Cell struct {
	Title  string
	Poster []byte
}

// Decode reads a poster, refusing anything that is not a JPEG, PNG or WebP
// of a sane size before any pixel is decoded.
func Decode(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("postergrid: not a picture: %w", err)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxSide || cfg.Height > maxSide ||
		int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, fmt.Errorf("postergrid: a %dx%d picture is refused", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("postergrid: unreadable picture: %w", err)
	}
	if b := img.Bounds(); b.Dx() < 1 || b.Dy() < 1 {
		return nil, errors.New("postergrid: empty picture")
	}
	return img, nil
}

// Compose draws up to Cells cells, left to right and top to bottom, and
// returns the JPEG. Cells beyond the ones given stay empty; cells beyond
// Cells are ignored. Posters are decoded one at a time, so memory stays at
// one poster plus the canvas. drawn is how many cells show a real poster.
func Compose(cells []Cell) (jpg []byte, drawn int, err error) {
	jpg, drawn, _, err = ComposeDetail(cells)
	return jpg, drawn, err
}

// ComposeDetail is Compose that also says why posters were left out: one
// short reason per unusable poster (its format or size, never its name).
func ComposeDetail(cells []Cell) (jpg []byte, drawn int, unusable []string, err error) {
	if len(cells) > Cells {
		cells = cells[:Cells]
	}
	dst := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(colBack), image.Point{}, draw.Src)
	for i := 0; i < Cells; i++ {
		r := cellRect(i)
		draw.Draw(dst, r, image.NewUniform(colEmpty), image.Point{}, draw.Src)
		if i >= len(cells) {
			continue
		}
		ok := false
		if len(cells[i].Poster) > 0 {
			if img, err := Decode(cells[i].Poster); err == nil {
				drawCover(dst, r, img)
				drawn++
				ok = true
			} else {
				unusable = append(unusable, err.Error())
			}
		}
		if !ok {
			drawTitle(dst, r, cells[i].Title)
		}
		drawBadge(dst, r, i+1)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, nil, fmt.Errorf("postergrid: encode: %w", err)
	}
	return buf.Bytes(), drawn, unusable, nil
}

// cellRect is where cell i (0 based) sits.
func cellRect(i int) image.Rectangle {
	x := gap + (i%Cols)*(cellW+gap)
	y := gap + (i/Cols)*(cellH+gap)
	return image.Rect(x, y, x+cellW, y+cellH)
}

// drawCover scales img to fill r, keeping its shape and cutting what sticks
// out evenly on both sides.
func drawCover(dst *image.RGBA, r image.Rectangle, img image.Image) {
	b := img.Bounds()
	src := b
	if b.Dx()*r.Dy() > b.Dy()*r.Dx() { // too wide: cut left and right
		w := max(1, b.Dy()*r.Dx()/r.Dy())
		src.Min.X = b.Min.X + (b.Dx()-w)/2
		src.Max.X = src.Min.X + w
	} else { // too tall: cut top and bottom
		h := max(1, b.Dx()*r.Dy()/r.Dx())
		src.Min.Y = b.Min.Y + (b.Dy()-h)/2
		src.Max.Y = src.Min.Y + h
	}
	draw.BiLinear.Scale(dst, r, img, src, draw.Over, nil) // Over: a see-through picture sits on the cell colour
}

// drawTitle writes the title in the middle of an empty cell, wrapped to the
// cell width and cut to the lines that fit.
func drawTitle(dst *image.RGBA, r image.Rectangle, title string) {
	face := basicfont.Face7x13
	perLine := (r.Dx() - 2*textPad) / (face.Advance * textScale)
	lineH := (face.Height + 3) * textScale
	maxLines := (r.Dy() - 2*textPad) / lineH
	lines := wrap(title, perLine, maxLines)
	y := r.Min.Y + (r.Dy()-len(lines)*lineH)/2
	for _, l := range lines {
		w := len([]rune(l)) * face.Advance * textScale
		drawText(dst, r, r.Min.X+(r.Dx()-w)/2, y, l, colText)
		y += lineH
	}
}

// drawBadge puts the cell's number in its top left corner, so the grid can
// be read against a numbered list.
func drawBadge(dst *image.RGBA, r image.Rectangle, n int) {
	face := basicfont.Face7x13
	s := strconv.Itoa(n)
	w := len(s)*face.Advance*textScale + 12
	h := face.Height*textScale + 8
	box := image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Min.Y+h)
	draw.Draw(dst, box, image.NewUniform(colBadge), image.Point{}, draw.Src)
	drawText(dst, box, box.Min.X+6, box.Min.Y+4, s, colText)
}

// drawText draws s with its top left corner at (x, y), each font pixel
// textScale pixels wide and high, never outside clip.
func drawText(dst *image.RGBA, clip image.Rectangle, x, y int, s string, col color.Color) {
	face := basicfont.Face7x13
	n := len([]rune(s))
	if n == 0 {
		return
	}
	mask := image.NewAlpha(image.Rect(0, 0, n*face.Advance, face.Height))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.P(0, face.Ascent)}
	d.DrawString(s)
	uni := image.NewUniform(col)
	for my := 0; my < mask.Rect.Dy(); my++ {
		for mx := 0; mx < mask.Rect.Dx(); mx++ {
			if mask.AlphaAt(mx, my).A < 0x80 {
				continue
			}
			px := image.Rect(x+mx*textScale, y+my*textScale, x+(mx+1)*textScale, y+(my+1)*textScale).Intersect(clip)
			draw.Draw(dst, px, uni, image.Point{}, draw.Src)
		}
	}
}

// wrap breaks s into at most maxLines lines of at most perLine runes, on
// spaces where it can. Text that does not fit ends with "...".
func wrap(s string, perLine, maxLines int) []string {
	if perLine < 4 || maxLines < 1 {
		return nil
	}
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, word := range strings.Fields(s) {
		w := []rune(word)
		for len(w) > 0 {
			room := perLine - len([]rune(cur))
			if cur != "" {
				room-- // the space before the word
			}
			if len(w) <= room {
				if cur != "" {
					cur += " "
				}
				cur += string(w)
				break
			}
			if cur != "" {
				flush()
				continue
			}
			// A word longer than a line is cut across lines.
			cur = string(w[:perLine])
			w = w[perLine:]
			flush()
		}
	}
	flush()
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		last := []rune(lines[maxLines-1])
		if len(last) > perLine-3 {
			last = last[:perLine-3]
		}
		lines[maxLines-1] = strings.TrimRight(string(last), " ") + "..."
	}
	return lines
}
