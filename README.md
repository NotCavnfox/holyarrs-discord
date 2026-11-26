<p align="center">
  <picture>
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/banner-light.svg">
    <img src="docs/assets/banner-dark.svg" alt="HolyArrs Discord plugin: your library, requests and ready pings in your Discord server" width="100%">
  </picture>
</p>

<p align="center">
  <a href="https://github.com/NotCavnfox/holyarrs-discord/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/NotCavnfox/holyarrs-discord/ci.yml?branch=main&style=flat-square&logo=github&logoColor=white&label=build" alt="Build"></a>
  <a href="https://github.com/NotCavnfox/holyarrs-discord/actions/workflows/codeql.yml"><img src="https://img.shields.io/github/actions/workflow/status/NotCavnfox/holyarrs-discord/codeql.yml?branch=main&style=flat-square&label=code%20scanning" alt="Code scanning"></a>
  <a href="https://github.com/NotCavnfox/holyarrs-discord/releases"><img src="https://img.shields.io/github/v/tag/NotCavnfox/holyarrs-discord?sort=semver&style=flat-square&label=version&color=3b3f8f" alt="Version"></a>
  <img src="https://img.shields.io/badge/HolyArrs-1.3.2%2B-3b3f8f?style=flat-square" alt="Needs HolyArrs 1.3.2 or newer">
  <a href="LICENSE"><img src="https://img.shields.io/badge/licence-MIT-3b3f8f?style=flat-square" alt="Licence: MIT"></a>
</p>

<h3 align="center">Your HolyArrs library, in your Discord server.</h3>

<p align="center">A Discord bot that runs inside <a href="https://github.com/HolyArrs/HolyArrs">HolyArrs</a> as a sandboxed plugin.<br>Members link their account with a one-time code, request titles from Discord,<br>and get a message when their request is ready to watch.</p>

<p align="center">
  <a href="#what">What it does</a> ·
  <a href="#setup">Set up</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#settings">Settings</a> ·
  <a href="#security">Security and privacy</a> ·
  <a href="#future">Future</a> ·
  <a href="#contributing">Contributing</a>
</p>

> [!NOTE]
> Needs HolyArrs 1.3.2 or newer. You bring your own Discord application and bot token.
> This plugin is not made by or affiliated with Discord or HolyArrs.

<a id="what"></a>

## ✨ What 1.0 does

| | |
|---|---|
| 🔗 **Linking** | `/link` gives a private one-time code. Type it in HolyArrs and the two accounts are linked. |
| 🖼️&nbsp;**Poster&nbsp;tiles** | Pinned tiles that update themselves: Recently added and Coming up as 3x3 poster grids, plus Recently watched and Top watchers. |
| 🙋 **Requests** | `/search` and `/request` ask for a movie or show as the linked person, under their own limits in HolyArrs. |
| 📋 **Status** | `/status` shows where your requests stand. |
| 🔔&nbsp;**Ready&nbsp;pings** | A DM when your request is approved, declined or available. `/privacy alerts:off` turns them off. |
| 📣&nbsp;**Announcements** | A post in a channel you choose when a title is added. |
| 🏠&nbsp;**Home&nbsp;card** | A Discord card on the HolyArrs Home page with the invite link and how to link. |
| 🕶️&nbsp;**Privacy&nbsp;first** | Names show next to watch activity only for members who opt in. |

It deletes nothing, and it has no voice features.

<details>
<summary><b>Each feature in detail</b></summary>

- **Linking.** A member runs `/link` and gets a private one-time code, valid for 10 minutes. They
  type it in HolyArrs under My account → Linked plugins. The bot notices within a few seconds and
  sends them a direct message (DM). The DM also says that they will get a message when a request
  of theirs is approved, declined or available, and that `/privacy alerts:off` turns those off.
- **Home card.** A "Discord" card on the HolyArrs Home page shows every signed-in user the invite
  link as text, how to link, and how many members have linked.
- **Owner page.** A "Discord" page under Plugins shows the owner the invite link and the steps to
  share with members.
- **Dashboard.** Four pinned messages ("tiles") in one channel, edited in place when their content
  changes:
  - Recently added: the 9 newest titles as a 3x3 grid of posters, with a numbered list under it in
    the same order.
  - Coming up: the next 9 releases in the coming 14 days, as the same kind of grid and list, with
    dates.
  - Recently watched: up to 10 titles and when each was last finished. No names.
  - Top watchers: people ranked by plays. A name shows only for a member who opted in with
    `/privacy show_watching:on`. Everyone else is counted in one closing line without a name.

  A title with no stored poster gets a dark cell with its name in it.
- **Announcements.** A post in a channel you choose when a title is added. More than 5 new titles
  within a minute become one summary post.
- **Requests.** `/search` and `/request` find a movie or show and request it as the linked person,
  under that person's own limits and approval rules in HolyArrs. `/status` shows where their
  requests stand.
- **Messages about requests.** A linked member gets a DM when a title they asked for is available.
  This covers requests made in the HolyArrs web app too. For a request made through the bot, the
  member also gets a DM when it is approved or declined. `/privacy alerts:off` turns all of these
  off.

</details>

<a id="setup"></a>

## 🚀 Set up

1. At discord.com/developers, create an application and add a bot. Copy the bot token. The bot
   needs no privileged intents.
2. Invite the bot to your server with the `bot` and `applications.commands` scopes and these
   permissions: View Channel, Send Messages, Embed Links, Attach Files, Manage Messages (to pin
   the tiles), and Manage Roles if you set a Linked role.
3. In HolyArrs, open the Plugins page, install Discord and approve its hosts and permissions.
4. Fill in the settings below. Bot token and Server id are required. The plugin waits until both
   are set, and restarts each time you save.
5. Share the invite link with your members. They join the server, run `/link`, and type the code
   in HolyArrs under My account → Linked plugins.

To copy a server, channel or role id in Discord, turn on Developer Mode, then right-click the item
and choose Copy ID.

<a id="commands"></a>

## 💬 Commands

| Command | Who | What it does |
|---|---|---|
| `/link` | Everyone | Starts linking and shows a private one-time code |
| `/unlink` | Linked | Removes your link from the bot and from HolyArrs |
| `/whoami` | Everyone | Shows which HolyArrs account you are linked to, and your privacy and alert choices |
| `/privacy` | Linked | Shows your current choices |
| `/privacy show_watching:<on/off>` | Linked | Chooses whether your name shows on Top watchers. Off by default |
| `/privacy alerts:<on/off>` | Linked | Chooses whether you get DMs about your requests: approved, declined and available. On by default |
| `/search query:<text>` | Linked | Searches movies and shows. Shows up to 5 results, each with a Request button |
| `/request title:<text>` | Linked | The same search. One clear match gets a single Confirm request button |
| `/status` | Linked | Shows your requests that are in progress, each with its state. With none in progress, your last 3 available requests. Only requests made through the bot |
| `/dashboard refresh` | Admin | Redraws every tile now, and makes a new message for a tile that was deleted |
| `/link-admin list` | Admin | Shows who is linked |
| `/link-admin remove member:<member>` | Admin | Removes that member's link, as their `/unlink` would |

Every reply to a command is visible only to the person who ran it. Request buttons work for 15
minutes, and only for the member who searched.

An admin is a member with the Admin role from the settings, or with Manage Server or Administrator
in Discord.

`/search` and `/request` exist only while Requests from Discord is on. `/dashboard` exists only
while Dashboard tiles is on and a dashboard channel is set.

<a id="settings"></a>

## ⚙️ Settings

| Setting | Default | Notes |
|---|---|---|
| Bot token | none | Required. Secret. From the Bot tab of your application |
| Server id | none | Required |
| Invite link | none | An `https://` invite. Shown as text on the Home card and on the owner page |
| Dashboard channel id | none | Channel for the four tiles. Empty turns the tiles off |
| Announcements channel id | none | Channel for new-title posts. Empty turns the posts off. DMs about available requests are still sent |
| Admin role id | none | Members with this role can use the admin commands |
| Linked role id | none | Optional. Role given on link and removed on unlink |
| Tile refresh (minutes) | 5 | 1 to 1440. Tiles also refresh a few seconds after a library or request event, and after a link or `/privacy` change |
| Dashboard tiles | On | The four tiles and `/dashboard refresh` |
| Announcements and ready pings | On | New-title posts and the DM when a request is available |
| Requests from Discord | On | `/search` and `/request`. `/status` and `/privacy` stay on |
| Discord card on Home | On | The Home card |

<a id="security"></a>

## 🔒 Security and privacy

- **Sandboxed.** HolyArrs builds the plugin from this source, signs it and runs it in a sandbox. It can reach only the two hosts and the permissions listed below, and write only in its own data folder.
- **Two hosts, both Discord.** No analytics, no tracking, no other website.
- **No red-warning permission.** It cannot delete or change your media.
- **Your token stays yours.** The bot token is a secret setting. It is never written to disk by the plugin or to the log.
- **Quiet logs.** The log holds counts only: no ids, no names, no titles, no invite link.
- **Found a problem?** Report it privately: [SECURITY.md](SECURITY.md).

### What it can reach

HolyArrs shows these lists before install. The plugin can reach nothing else.

| Host | Why |
|---|---|
| `discord.com` | The Discord API: commands, messages, DMs, pins and roles |
| `gateway.discord.gg` | The live connection that delivers slash commands and button presses. The bot reconnects to this same address |

| HolyArrs permission | Why |
|---|---|
| `library.read` | Title details, watch totals, the watched list, the title search behind `/search`, and stored posters (it covers `library.artwork.read`) |
| `calendar.read` | The Coming up tile |
| `discover.home.read` | The Recently added tile, read from Home's "Just arrived" row |
| `requests.read` | Who asked for a title, to send the "available" DM |
| `requests.change` | Making a request as the linked person |
| `requests.users.read` | Link codes, the list of links, and the HolyArrs user name shown to a linked member |
| `pages.add` | The owner page under Plugins |
| `pages.home-widget` | The Discord card on Home |

| Event | Why |
|---|---|
| `library.add` | New-title posts, the "available" DM, and a tile refresh |
| `request.made` | A tile refresh |
| `request.approved`, `request.declined` | Keeping `/status` current, the approved and declined DMs, and a tile refresh |
| `request.available` | The "available" DM, keeping `/status` current, and a tile refresh |

The plugin asks for no extra folders and no red-warning permission.

### What it stores

The plugin keeps small JSON files in its own data folder:

| File | Holds |
|---|---|
| `links.json` | Discord id, HolyArrs user id and name, and the privacy and alert choices |
| `pending.json` | Discord id and expiry of each link code that is waiting. Never the code |
| `tiles.json` | The message id and a content hash for each tile |
| `announced.json` | Which titles were posted and which "available" DMs were sent, so nothing is sent twice |
| `myrequests.json` | Requests made through the bot, up to 50 per member |

- The bot token and link codes are never written to disk or to the log. The log holds counts
  only: no Discord ids, no HolyArrs user ids, no channel, role or server ids and no invite link.
- Nothing is sent anywhere except Discord and your own HolyArrs.
- Posters in the grid tiles are read from HolyArrs at its small size (250 px) and uploaded to
  Discord as one picture. The bot
  fetches no picture from any other host. In a new-title post or a search result, a poster that
  HolyArrs holds only as an outside `https` link is passed to Discord as a link, and Discord loads
  it.
- A member's name appears next to watch activity only if they turn it on with
  `/privacy show_watching:on`.
- A linked member gets a DM when a request of theirs is approved, declined or available unless
  they turn these off with `/privacy alerts:off`. `/privacy` with no option shows both choices.
- Turning `show_watching` off, or unlinking, takes the name off Top watchers within seconds.
- `/unlink` removes a member's link from the bot and from HolyArrs. A link removed in HolyArrs
  (My account → Linked plugins) is removed from the bot within 5 minutes. When a link goes, by
  `/unlink`, `/link-admin remove` or in HolyArrs, the member's entries in `myrequests.json` go
  with it.
- Removing the plugin in HolyArrs deletes its data folder.

<a id="limits"></a>

## 🧭 Known limits

<details>
<summary><b>The full list</b></summary>

- `/status` lists only requests made through the bot. HolyArrs gives a plugin no way to read a
  person's requests, so requests made in the HolyArrs web app are not shown. The "available" DM
  does cover them.
- `/status` knows four states: waiting for approval, approved, available and declined. HolyArrs
  does not tell a plugin whether an approved request is being searched for or downloaded.
- `/privacy alerts` is one switch for every request DM: approved, declined and available. They
  cannot be chosen one by one.
- A tile message deleted in Discord comes back when its content next changes, or at once with
  `/dashboard refresh`.
- Posters in the grids are HolyArrs's 250 px copies, drawn larger, so they look soft.
- If Discord cannot be reached at start (or the bot token is wrong), the plugin stays running and
  tries again, waiting up to 2 minutes between tries. The log has one line per failed try.
- Poster tiles use only posters HolyArrs has stored. A title whose poster is still an outside link
  (`image.tmdb.org`, `static.tvmaze.com`) gets a cell with its name, because those hosts are not
  in the plugin's host list. This mostly affects Coming up.
- Recently added follows Home's "Just arrived" row: titles from about the last two weeks.
- Recently watched shows finished titles only. A show counts once every episode with a file was
  watched. There is no "now playing", and HolyArrs never tells a plugin who watched which title.
- One Discord member links to one HolyArrs user. If two HolyArrs users type codes from the same
  member, the bot keeps the older link and removes the other.
- Linking needs HolyArrs 1.3.2. On an older HolyArrs `/link` and `/unlink` say that an update is
  needed.
- One Discord server per install.

</details>

<a id="future"></a>

## 🗺️ Future development

Planned, and possible with today's HolyArrs API. No dates. Each goal has an issue here; comment or
react there to say you want it.

| Goal | Notes | Tracking |
|---|---|---|
| Weekly recap post, `/leaderboard` | From the per-person watch totals | [#1](https://github.com/NotCavnfox/holyarrs-discord/issues/1) |
| `/random` | A random title from the library | [#2](https://github.com/NotCavnfox/holyarrs-discord/issues/2) |
| Movie night poll and Discord scheduled event | Library search plus Discord polls | [#3](https://github.com/NotCavnfox/holyarrs-discord/issues/3) |
| Delete vote (`/purge-vote`) | An admin starts a vote from the clean-up list and confirms the result. Needs the red-warning `library.delete` permission, so it ships as its own version for owners to approve | [#4](https://github.com/NotCavnfox/holyarrs-discord/issues/4) |
| Poster trivia and automatic roles | Small fun features; roles need Manage Roles in Discord | [#5](https://github.com/NotCavnfox/holyarrs-discord/issues/5) |
| Sharper posters | Read full-size artwork and scale it down, instead of the 250 px copy | [#10](https://github.com/NotCavnfox/holyarrs-discord/issues/10) |

<a id="nice"></a>

## 🤝 Nice to haves (need HolyArrs changes)

These cannot be built until HolyArrs offers more to plugins.

| Goal | What is missing in HolyArrs | HolyArrs issue | Tracking |
|---|---|---|---|
| One-click "Connect" from My account with Discord sign-in, and joining the server in the same step | Plugins cannot receive an OAuth callback | [HolyArrs/HolyArrs#828](https://github.com/HolyArrs/HolyArrs/issues/828) | [#6](https://github.com/NotCavnfox/holyarrs-discord/issues/6) |
| `/status` showing requests made in the HolyArrs web app, and download progress | A plugin cannot list a person's requests: `GET /asks` answers a plugin with an empty list and refuses `X-Holyarrs-For-User` on reads | Not filed yet | [#8](https://github.com/NotCavnfox/holyarrs-discord/issues/8) |
| Posters for upcoming titles HolyArrs has not stored yet | Those posters are outside links. Showing them would need outside image hosts in the plugin's host list, or HolyArrs storing or serving them for plugins | Not filed yet | [#11](https://github.com/NotCavnfox/holyarrs-discord/issues/11) |
| Back off for exactly as long as HolyArrs asks | Plugin 429 replies for "calling too often" carry no `Retry-After` | Not filed yet | [#7](https://github.com/NotCavnfox/holyarrs-discord/issues/7) |
| A DM when a request is approved, for requests made in the web app | Request events name no person, and `/asked-by` only takes a library title id, not a request id | Not filed yet | [#9](https://github.com/NotCavnfox/holyarrs-discord/issues/9) |
| A clickable invite on the Home card | Home card links can only point inside HolyArrs | Not filed yet | [#12](https://github.com/NotCavnfox/holyarrs-discord/issues/12) |
| A member-facing page in HolyArrs | Plugin pages open for the owner only | Not filed yet | [#13](https://github.com/NotCavnfox/holyarrs-discord/issues/13) |
| `/wrapped`, personal recently-watched | Watch data for plugins is totals per person and last-watched per title, never who watched which title | Not filed yet | [#14](https://github.com/NotCavnfox/holyarrs-discord/issues/14) |

Not planned: "now playing" and live playback activity. HolyArrs has no now-playing route and no
playback events for plugins.

<a id="contributing"></a>

## 🧑‍💻 Contributing

Open an issue before anything bigger than a small fix. Keep pull requests small: one fix or one
feature each. CI must pass before a merge. The details are in [CONTRIBUTING.md](CONTRIBUTING.md).
Report security problems as described in [SECURITY.md](SECURITY.md), not in a public issue.
Everyone taking part follows the [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

<details>
<summary><b>Develop and build</b></summary>

You need Go, the version in `go.mod`. The tests never touch the network.

```sh
go vet ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -mod=readonly -o build/amd64/plugin .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -mod=readonly -o build/arm64/plugin .
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -mod=readonly -o build/armv7/plugin .
```

To try a build on your own server, use HolyArrs developer mode: copy `holyarrs-plugin.json` and the
built `plugin` file into `<config folder>/plugins/_dev/discord/`, then choose Install from folder on
the Plugins page.

Design: [ARCHITECTURE.md](ARCHITECTURE.md). HolyArrs wire shapes:
[docs/holyarrs-api.md](docs/holyarrs-api.md).

</details>

<details>
<summary><b>Self-check</b></summary>

On every start, also while the plugin is still waiting for its settings, it tries each thing it
depends on once, read-only, and logs one line per check: `selfcheck check=<name> ok=true|false
n=<count> ms=<duration>`, with `err=<reason>` when it failed, then `selfcheck done passed=X
failed=Y`. The checks are `users`, `recently_added`, `watch_people`, `recently_watched`,
`upcoming`, `search`, `links` (HolyArrs) and `discord` (Discord reachable through the proxy; no
bot token needed). Only counts are logged, never names, titles or ids. `links` failing with
"needs a newer HolyArrs" is expected on HolyArrs 1.3.1. A failed check never stops the plugin.

</details>

<a id="releases"></a>

## 📦 Releases

Versions are plain tags like `1.0.0`, with no `v`. HolyArrs builds the plugin from this source at
the tagged commit, signs it and hosts it. So a release here carries no binaries, only notes.

- What changed in each version: [CHANGELOG.md](CHANGELOG.md).
- Release notes: [GitHub Releases](https://github.com/NotCavnfox/holyarrs-discord/releases).
- Install and update from the Plugins page in HolyArrs. Installing again takes the newest listed
  version.

## Licence

MIT. See [LICENSE](LICENSE).
