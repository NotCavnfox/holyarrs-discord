# HolyArrs Discord plugin

A Discord bot that runs inside HolyArrs (1.3.2 or newer) as a sandboxed plugin. It is not made by
or affiliated with Discord or HolyArrs.

## What it does

- **Linking**: a member runs `/link`, gets a private one-time code and types it in HolyArrs under
  My account → Linked plugins. The bot notices within a few seconds and messages them. The message
  also says that they will get a DM each time one of their requests is available, and how to turn
  that off.
- **Home card**: a "Discord" card on the HolyArrs Home page shows every signed-in user the invite
  link, how to link, and how many members have linked.
- **Dashboard**: four self-updating pinned tiles: Recently added, Recently watched, Top watchers
  (names only for members who opt in with `/privacy`), Coming up. Recently added and Coming up
  show a 3x3 grid of posters (the 9 newest titles, the next 9 releases) with a numbered list
  under it in the same order. A title with no stored poster gets a dark cell with its name.
- **Announcements**: a post when a title is added; a DM when a title you asked for is available
  (`/privacy alerts:off` turns the DM off).
- **Requests**: `/search`, `/request`, `/status`. Requests are made as the linked person, under
  their own limits.

It does not delete anything, and it has no voice features.

## Commands

| Command | Who | What it does |
|---|---|---|
| `/link` | Everyone | Starts linking and shows a private one-time code |
| `/unlink` | Linked | Removes your link from the bot and from HolyArrs |
| `/whoami` | Everyone | Shows which HolyArrs account you are linked to, and your privacy and alert choices |
| `/privacy` | Linked | Shows your current choices |
| `/privacy show_watching:<on/off>` | Linked | Chooses whether your name shows on Top watchers. Off by default |
| `/privacy alerts:<on/off>` | Linked | Chooses whether you get a DM when a request of yours is available. On by default |
| `/search query:<text>` | Everyone | Searches movies and shows |
| `/request title:<text>` | Linked | Requests a title as you |
| `/status` | Linked | Shows your requests that are not finished, each with its state. With none in progress, your last 3 available requests |
| `/dashboard refresh` | Admin | Redraws the tiles now |
| `/link-admin list` | Admin | Shows who is linked |
| `/link-admin remove member:<member>` | Admin | Removes that member's link, as their `/unlink` would |

All replies to commands are visible only to the person who ran them.

## Set up

1. At discord.com/developers, create an application and add a bot. Copy the bot token.
2. Invite the bot to your server with the `bot` and `applications.commands` scopes and these
   permissions: View Channel, Send Messages, Embed Links, Attach Files, Manage Messages (to pin
   tiles), and Manage Roles if you set a Linked role.
3. In HolyArrs, install the plugin and fill in its settings.

| Setting | Notes |
|---|---|
| Bot token (secret) | From your application's Bot tab |
| Server id | Required |
| Invite link | Shown on the Home card and on the owner's Discord page in HolyArrs |
| Dashboard / Announcements / Moderator channel id | Leave empty to turn that part off |
| Admin role id | Members with this role can use the admin commands |
| Linked role id | Given to members once they link. Optional |
| Tile refresh (minutes) | Default 5 |
| Dashboard tiles, Announcements, Requests, Discord card on Home | Toggles, on by default |

## Hosts and permissions

Hosts: `discord.com`, `gateway.discord.gg`, `*.discord.gg`, `cdn.discordapp.com`.

HolyArrs permissions: `library.read`, `calendar.read`, `discover.home.read`, `requests.read`,
`requests.change`, `requests.users.read`, `pages.add`, `pages.home-widget`. Events: `library.add`
and `request.*`. Posters are read from HolyArrs itself under `library.read`, which covers
`library.artwork.read`; the bot fetches no picture from any other host.

## Data and privacy

Kept in the plugin data folder as small JSON files: links (Discord id, HolyArrs user id and name,
privacy and alert choices), tile message ids, announcement dedupe keys, requests made through the
bot.
No tokens or link codes are written to disk. `/unlink` removes a member's link from the bot and
from HolyArrs. A link removed in HolyArrs (My account → Linked plugins) is removed from the bot
within 5 minutes.

Nothing is sent anywhere except Discord. A member's name appears next to watch activity only if
they turn it on with `/privacy show_watching:on`. A linked member gets a DM when a request of
theirs is available unless they turn it off with `/privacy alerts:off`. `/privacy` with no option
shows both choices.

## Known limits

- `/status` lists only requests made through the bot. HolyArrs gives a plugin no way to read a
  person's requests, so requests made in the HolyArrs web app are not shown. The "available" DM
  does cover them.
- `/status` knows four states: waiting for approval, approved, available and declined. HolyArrs
  does not tell a plugin whether an approved request is being searched for or downloaded.
- Poster tiles use only posters HolyArrs has stored. A title whose poster is still an outside
  link (`image.tmdb.org`, `static.tvmaze.com`) gets a cell with its name, because those hosts are
  not in the plugin's host list.
- Watch tiles show totals per person and finished titles per account. There is no "now playing".

## Future goals

What is planned, and what each part needs from HolyArrs first.

**Can be built today**

| Goal | Notes |
|---|---|
| Weekly recap post, `/leaderboard` | From the per-person watch totals |
| `/random` | A random title from the library |
| Movie night poll and Discord scheduled event | Library search plus Discord polls |
| Delete vote (`/purge-vote`) | An admin starts a vote from the clean-up list and confirms the result. Needs the red-warning `library.delete` permission, so it ships as its own version for owners to approve |

**Needs a HolyArrs API change**

| Goal | What is missing | Issue |
|---|---|---|
| One-click "Connect" from My account with Discord sign-in, and joining the server in the same step | Plugins cannot receive an OAuth callback | [HolyArrs/HolyArrs#828](https://github.com/HolyArrs/HolyArrs/issues/828) |
| Back off for exactly as long as HolyArrs asks | Plugin 429 replies carry no `Retry-After` | Not filed yet |
| `/status` showing every request, not only ones made through the bot, and download progress | `GET /asks` answers a plugin with an empty list and refuses `X-Holyarrs-For-User` on reads | Not filed yet |
| A ping when a request is approved before the title exists | `/asked-by` only takes a title id, not a request id | Not filed yet |
| Sharper posters in embeds | Artwork is served at 250 px or full size only | Not filed yet |
| A clickable invite on the Home card | Home card links can only point inside HolyArrs | Not filed yet |
| A member-facing page in HolyArrs | Plugin pages are owner-only | Not filed yet |
| `/wrapped`, personal recently-watched | No per-person watch history for plugins | Not filed yet |

**Not planned**: "now playing" and live playback activity. HolyArrs does not offer playback events
to plugins.

## Develop

```sh
go vet ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -mod=readonly -o build/amd64/plugin .
```

To try a build on your own server, use HolyArrs developer mode: copy `holyarrs-plugin.json` and the
built `plugin` file into `<config folder>/plugins/_dev/discord/`, then choose Install from folder on
the Plugins page.

### Self-check

On every start, also while the plugin is still waiting for its settings, it tries each thing it
depends on once, read-only, and logs one line per check: `selfcheck check=<name> ok=true|false
n=<count> ms=<duration>`, with `err=<reason>` when it failed, then `selfcheck done passed=X
failed=Y`. The checks are `users`, `recently_added`, `watch_people`, `recently_watched`,
`upcoming`, `search`, `links` (HolyArrs) and `discord` (Discord reachable through the proxy; no
bot token needed). Only counts are logged, never names, titles or ids. `links` failing with
"needs a newer HolyArrs" is expected on HolyArrs 1.3.1. A failed check never stops the plugin.

Design: [ARCHITECTURE.md](ARCHITECTURE.md). HolyArrs wire shapes: [docs/holyarrs-api.md](docs/holyarrs-api.md).

## Licence

MIT. See [LICENSE](LICENSE).
