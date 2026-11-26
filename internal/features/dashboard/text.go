package dashboard

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// Discord limits. Counted in runes, which is what Discord shows people.
const (
	maxEmbedTitle = 256
	maxEmbedDesc  = 4096
	maxFieldName  = 256
	maxFieldValue = 1024
	maxFields     = 25
	maxFooter     = 2048
	maxEmbedTotal = 6000
	maxEmbeds     = 10
	maxNameLen    = 100 // a title or person name inside a line
)

var mdEscaper = strings.NewReplacer(
	`\`, `\\`, `*`, `\*`, `_`, `\_`, `~`, `\~`, "`", "\\`", `|`, `\|`,
	`>`, `\>`, `<`, `\<`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`,
	`#`, `\#`, `@`, "@​",
)

// escapeMD makes text from HolyArrs safe to place in a Discord message: no
// markdown, no mentions, no line breaks.
func escapeMD(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return mdEscaper.Replace(s)
}

// truncate cuts s to at most n runes, ending with "…" when it was cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	cut := strings.TrimRight(string(r[:n-1]), `\`)
	return cut + "…"
}

// safeName truncates first and escapes second, so an escape is never cut in half.
func safeName(s string) string {
	return escapeMD(truncate(strings.Join(strings.Fields(s), " "), maxNameLen))
}

// titleLabel is "**Name** (Year)".
func titleLabel(name string, year int) string {
	s := "**" + safeName(name) + "**"
	if year > 0 {
		s += fmt.Sprintf(" (%d)", year)
	}
	return s
}

func kindLabel(k core.Kind) string {
	switch k {
	case core.KindMovie:
		return "Movie"
	case core.KindShow:
		return "Show"
	case core.KindAlbum:
		return "Album"
	}
	return ""
}

func relTime(t time.Time) string {
	return fmt.Sprintf("<t:%d:R>", t.Unix())
}

// joinLines joins whole lines with newlines while they fit in limit runes and
// ends with "…" when some were left out.
func joinLines(lines []string, limit int) string {
	var b strings.Builder
	used := 0
	for i, l := range lines {
		need := utf8.RuneCountInString(l)
		if i > 0 {
			need++
		}
		reserve := 0
		if i < len(lines)-1 {
			reserve = 2 // room for "\n…"
		}
		if used+need+reserve > limit {
			if i == 0 {
				return truncate(l, limit)
			}
			b.WriteString("\n…")
			return b.String()
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
		used += need
	}
	return b.String()
}

// clampEmbed forces an embed inside Discord's limits.
func clampEmbed(e core.Embed) core.Embed {
	e.Title = truncate(e.Title, maxEmbedTitle)
	e.Description = truncate(e.Description, maxEmbedDesc)
	e.Footer = truncate(e.Footer, maxFooter)
	n := min(len(e.Fields), maxFields)
	fields := make([]core.Field, 0, n)
	total := utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description) + utf8.RuneCountInString(e.Footer)
	for _, f := range e.Fields[:n] {
		f.Name = truncate(f.Name, maxFieldName)
		f.Value = truncate(f.Value, maxFieldValue)
		total += utf8.RuneCountInString(f.Name) + utf8.RuneCountInString(f.Value)
		fields = append(fields, f)
	}
	for total > maxEmbedTotal && len(fields) > 0 {
		last := fields[len(fields)-1]
		total -= utf8.RuneCountInString(last.Name) + utf8.RuneCountInString(last.Value)
		fields = fields[:len(fields)-1]
	}
	if total > maxEmbedTotal {
		e.Description = truncate(e.Description, utf8.RuneCountInString(e.Description)-(total-maxEmbedTotal))
	}
	if len(fields) == 0 {
		fields = nil
	}
	e.Fields = fields
	return e
}

// clampMessage applies clampEmbed to every embed and keeps at most 10.
func clampMessage(m core.Message) core.Message {
	if len(m.Embeds) > maxEmbeds {
		m.Embeds = m.Embeds[:maxEmbeds]
	}
	out := make([]core.Embed, len(m.Embeds))
	for i, e := range m.Embeds {
		out[i] = clampEmbed(e)
	}
	if len(out) == 0 {
		out = nil
	}
	m.Embeds = out
	m.Content = truncate(m.Content, 2000)
	return m
}
