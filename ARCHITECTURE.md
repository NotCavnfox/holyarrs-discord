# Architecture

One pure-Go binary (`CGO_ENABLED=0`) run by HolyArrs in its plugin sandbox. It never listens on
a port: it dials out to the Discord gateway and long-polls HolyArrs.

## Shape: ports and adapters

```text
                 +--------------------- features ----------------------+
                 | linking  dashboard  announce  requests  (vote, fun) |
                 +---------------------------+-------------------------+
                                             | depends only on
                                   +---------v---------+
                                   |   internal/core   |  domain types + ports
                                   +---^-----^-----^---+
                      implements      |     |     |      implements
        +-----------------------------+     |     +---------------------------+
        | internal/holyarrs                 |                internal/discordgw |
        | HTTP client, hooks, events        |      discordgo gateway + REST    |
        +-----------------------------------+----------------------------------+
                    internal/store (JSON files)   internal/links (link book)
                    internal/config (env)         internal/app (bus, router, runner)
                                      main.go wires it all
```

Rules:
- `internal/core` imports only the standard library. Everything else imports `core`.
- Features import `core` only: never `discordgo`, never the HolyArrs wire format, never each other.
- Adapters translate wire shapes to `core` types and wire errors to `core.Err*`.
- Only `main.go` names concrete types. Only `internal/discordgw` imports `discordgo` or `gorilla/websocket`.
- A new feature is a new package under `internal/features/` plus one line in `main.go`.

## Packages

| Package | Owns |
|---|---|
| `internal/core` | Domain types, ports (`HolyArrs`, `Discord`, `Store`, `Links`, `Events`), `Feature`, `Deps` |
| `internal/config` | Reads `HOLYARRS_PLUGIN_*` into `core.Config`, validates, never logs secrets |
| `internal/store` | `core.Store`: `<data>/<name>.json`, write to temp file then rename |
| `internal/links` | `core.Links` over the store (`links.json`), mutex-guarded, indexed both ways |
| `internal/holyarrs` | `core.HolyArrs`: Bearer client, 429 backoff, 6-call concurrency cap, stdin events, pages hook, Home widget |
| `internal/discordgw` | `core.Discord`: gateway session, command registration, interaction mapping |
| `internal/selfcheck` | Read-only startup pass over the `HolyArrs` reads and the Discord probe; one log line per check, counts only |
| `internal/app` | Event bus (fan-out, drop when slow), interaction router, feature runner, shutdown |
| `internal/features/linking` | `/link`, `/unlink`, `/whoami`, `/link-admin`, the link reconcile loop, Linked role, the plugin page, the Home widget |
| `internal/features/dashboard` | Self-updating tiles: one pinned message per tile, edited in place |
| `internal/features/announce` | New-title posts; ready DMs to whoever asked for the title |
| `internal/features/requests` | `/me`, `/request`, `/search`, `/privacy`; approved/declined DMs for bot-made requests |
| `internal/features/vote` (1.1) | `/purge-vote`, ballots, admin confirm, audit log |
| `internal/features/fun` (1.2) | recaps, leaderboards, movie night, games, roles |

## Runtime

`main` -> config -> store -> links -> adapters -> features -> `app.Run`:
1. `app` collects every feature's commands and opens Discord with them.
2. One goroutine pumps `HolyArrs.Events` (stdin lines) into the bus; each feature gets its own channel.
3. Each feature's `Run` gets its own goroutine. A feature that fails is logged and restarted
   with backoff; it never takes the others down.
4. Slash commands go to the owning `Command.Handle`. Button presses go to the feature named by
   the `CustomID` prefix (`"<feature>:<action>[:<arg>]"`).
5. On SIGTERM or stdin close: cancel the context, wait up to 10 s, save state, close Discord, exit 0.

## State (plugin data folder)

| File | Owner | Holds |
|---|---|---|
| `links.json` | links | Discord id, HolyArrs user id and name, privacy flag |
| `pending.json` | linking | Discord id and expiry of each code that is waiting to be typed (never the code) |
| `tiles.json` | dashboard | message id per tile, last content hash |
| `announced.json` | announce | announced and pinged keys (dedupe across restarts) |
| `myrequests.json` | requests | requests made through the bot, per member |
| `votes.json` | vote | open and closed ballots |

Everything is rebuildable. Links are rebuilt from HolyArrs on the next reconcile pass, but the
privacy choice is then back to hidden; a lost tile file just makes new messages.

## Linking

HolyArrs holds the links and is the source of truth. A plugin can list its own links
(`Links`) and remove one (`RemoveLink`); `links.json` is a copy that also keeps the name and the
privacy choice.

1. `/link` takes no options. It refuses a member who is already linked (they run `/unlink`
   first). Otherwise it asks for a code with the member's Discord id as the external id
   (`NewLinkCode(discordID)`), notes the member and the expiry in `pending.json`, and shows the
   code to that member only. It is valid 10 minutes. Running `/link` again gives a fresh code;
   HolyArrs replaces the old one. Any number of members can be waiting at once.
2. The member types the code in HolyArrs under My account, Linked plugins. HolyArrs then holds a
   link from that user to the Discord id.
3. A reconcile loop reads `Links` once at start, every 5 seconds while a code is waiting, and
   every 5 minutes otherwise. `/link` and `/unlink` wake it. Each pass:
   - A link in HolyArrs whose external id is not linked in the bot is adopted: the name comes
     from `Users` (the id as text if the user is not listed), the link is saved with the name
     hidden from watch activity, the pending entry is dropped, the Linked role is given and the
     member gets a DM. No pending entry is needed, because the external id can only come from a
     code this plugin asked for. External ids that are not 15 to 21 digits are ignored.
   - HolyArrs keeps one link per user, so two users can hold the same Discord id. The bot keeps
     the one it already has, else the oldest (lowest user id on a tie), and calls `RemoveLink`
     for the others. This also covers a linked member typing a code from a second account.
   - A link in the bot that HolyArrs no longer holds is removed, with the Linked role.
   - Pending entries past their expiry are dropped.
4. Nothing is removed unless `Links` answered with a list. On an error the pass changes nothing.
5. `/unlink` calls `RemoveLink` first and removes the local link only when that worked.
   `/link-admin remove` does the same for another member; `/link-admin list` shows who is linked.

A HolyArrs without these routes answers 404 or 405, which the adapter reports as
`core.ErrUnsupported`. Then `/link` and `/unlink` say that a newer HolyArrs is needed, existing
links are left alone, and the loop logs one warning and stays on the 5-minute wait.

Link codes appear only in the private reply to `/link`. They are not stored or logged.

The plugin page is an owner-only setup note: HolyArrs opens plugin pages for the owner only and
its sandbox blocks external links, so the invite is shown as text to share.

Members are reached through the Home widget instead (`ENABLE_HOME_WIDGET`, on by default): a
"Discord" card with the invite as text, a "Link your account" row that opens `/settings/account`,
and the number of linked members. `Run` publishes it on start and again when the link count
changes (checked after each reconcile pass and each `/unlink`); a failed publish is logged and retried with backoff (5 s
doubling to 5 min) and never stops the feature. HolyArrs keeps the card until the plugin stops, so
there is no keep-alive.

## What HolyArrs does and does not give a plugin

- Events arrive as JSON lines on stdin (`access.events`); stdin closing means stop.
- Watch data: totals per person, and last-watched per title. Never who watched which title, and
  no now-playing. Hence the tiles: Recently added, Recently watched (titles only), Top watchers
  (opt-in names), Coming up.
- Requests cannot be listed. The bot knows requests it made itself (`myrequests.json`) and can
  ask who requested a library title (`AskedBy`), which drives ready pings.
- Pages are for the owner only. A Home widget (one card of plain text rows, links to HolyArrs
  pages only) is shown to every signed-in user; it is the only on-site way to reach members.
- Exact wire shapes: `docs/holyarrs-api.md`.

## Limits designed for

- HolyArrs: about 600 calls a minute, 8 at once. The client queues behind a semaphore and backs
  off on 429. Dashboard reads are cached per refresh so tiles share one fetch.
- Discord: edits to tiles only when the rendered content hash changes.
- 512 MiB, 1 CPU: no in-memory library copy; posters fetched on demand with a small LRU.
- No UDP: no voice features.

## Privacy and safety

- A person's name appears next to watch activity only when their link has `ShowWatching`.
- Deletion (1.1) needs an admin button press in Discord for each run and is logged to the mod channel.
- The bot token lives only in the secret setting; it is never written to disk or logs.
- Text from HolyArrs and Discord is data. Titles are escaped before they go into messages.

## Testing

Features are tested against in-memory fakes of the ports; adapters are tested with
`httptest` (HolyArrs) and mapping tests (Discord). Nothing in `go test` touches the network.
