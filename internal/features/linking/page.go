package linking

import (
	"html"
	"net/url"
	"strings"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	pageID   = "discord"
	pageFile = "index.html"
)

// Pages lists the plugin pages this feature serves.
func (f *Feature) Pages() []core.Page {
	return []core.Page{{ID: pageID, Title: "Discord", File: pageFile}}
}

// ServePage answers a page file request. It matches core.PageHandler.
func (f *Feature) ServePage(file string) ([]byte, string, bool) {
	if strings.TrimPrefix(file, "/") != pageFile {
		return nil, "", false
	}
	return f.page, "text/html; charset=utf-8", true
}

// safeInvite returns the invite link when it is an absolute http(s) URL, and
// "" otherwise, so a mistyped setting is never shown as something to share.
func safeInvite(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return raw
}

// renderPage builds the page. Only the server owner can open plugin pages and
// their sandbox blocks links and popups, so the invite is plain text to copy.
func renderPage(inviteURL string) []byte {
	invite := `<p class="muted">No invite link is set yet. Set the <strong>Invite link</strong> setting of this plugin to a Discord invite (https://discord.gg/...) and it will show here.</p>`
	if u := safeInvite(inviteURL); u != "" {
		invite = `<p>Invite link (select and copy):</p>
<pre class="copy">` + html.EscapeString(u) + `</pre>`
	}
	return []byte(pageHead + invite + pageTail)
}

const pageHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Discord</title>
<style>
:root {
  color-scheme: light dark;
  --bg: #ffffff; --fg: #1c1f24; --muted: #5b6370; --card: #f3f4f7;
  --line: #d9dce3; --accent: #5865f2; --accent-fg: #ffffff;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #15171c; --fg: #e8eaee; --muted: #a2a9b5; --card: #1f232b;
    --line: #333945; --accent: #7983f5; --accent-fg: #0d0f14;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 24px 16px; background: var(--bg); color: var(--fg);
  font: 16px/1.55 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
}
main { max-width: 640px; margin: 0 auto; }
h1 { font-size: 1.5rem; margin: 0 0 4px; }
h2 { font-size: 1.05rem; margin: 28px 0 8px; }
p { margin: 8px 0; }
.muted { color: var(--muted); }
.copy {
  margin: 8px 0 4px; padding: 10px 14px; border-radius: 8px; background: var(--card);
  border: 1px solid var(--line); font: 0.95em/1.4 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  white-space: pre-wrap; overflow-wrap: anywhere; user-select: all; -webkit-user-select: all;
}
ol { margin: 0; padding-left: 1.4em; }
li { margin: 8px 0; }
code {
  padding: 1px 6px; border-radius: 4px; background: var(--card);
  border: 1px solid var(--line); font-size: 0.95em;
}
.note {
  margin-top: 28px; padding: 12px 14px; border-radius: 8px;
  background: var(--card); border: 1px solid var(--line);
}
.note h2 { margin-top: 0; }
</style>
</head>
<body>
<main>
<h1>Discord</h1>
<p class="muted">Only you, the server owner, can open this page.</p>
<h2>Share this with your members</h2>
`

const pageTail = `
<h2>What members do</h2>
<ol>
<li>Join the Discord server with the invite link.</li>
<li>Run <code>/link</code> in Discord. The bot replies with a one-time code that only they can see.</li>
<li>Sign in to HolyArrs and type the code under <strong>My account &rarr; Linked plugins</strong> before it expires (10 minutes).</li>
</ol>
<p class="muted">The bot messages them once the link is confirmed. <code>/unlink</code> removes it at any time, in the bot and in HolyArrs.</p>
<div class="note">
<h2>Privacy</h2>
<p>A member's name is not shown next to what they watch unless they opt in. They can turn it on or off at any time with <code>/privacy</code>.</p>
</div>
</main>
</body>
</html>
`
