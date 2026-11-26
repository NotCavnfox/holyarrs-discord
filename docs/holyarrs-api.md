# HolyArrs wire contracts used by this plugin

From a read-only scan of the HolyArrs source (commit e0d44c78, 2026-11-24; the two link routes
from commit a73d48c8; scopes, artwork and `GET /asks` checked against a97f944e). Paths in brackets are
under `app/backend/` in that repo. Items marked *inferred* were not confirmed; verify in the source
or at run time. All calls: `Authorization: Bearer $HOLYARRS_PLUGIN_TOKEN` to
`$HOLYARRS_PLUGIN_TARGET` (`http://127.0.0.1:<port>`). Errors are `{"error":"<text>"}`.

## Environment
`HOLYARRS_PLUGIN_TARGET`, `HOLYARRS_PLUGIN_TOKEN` (`hap_…`, new each start; present only when
actions are approved and the sandbox locks are active), `HOLYARRS_PLUGIN_DATA` (= `HOME`),
`HOLYARRS_PLUGIN_SETTING_<KEY>`, `HTTPS_PROXY`/`HTTP_PROXY`/`ALL_PROXY` (+ lowercase) =
`http://p:<token>@127.0.0.1:<port>`, `NO_PROXY=localhost,127.0.0.1`. Variables starting with
`GO` are stripped. No UDP, so no in-process DNS: let the proxy resolve names.

Proxy: HTTP CONNECT, ports 443/80, long-lived tunnels allowed, each direction closes after
**10 minutes of silence**, 64 concurrent connections, 10 s dial timeout.

## Limits
600 calls per fixed minute and **8 open calls** per plugin, long-polls included. 429 body
`{"error":"…calling too often…"}` or `"…too many calls open…"`, **no Retry-After**.

## Events: JSON lines on stdin
Declare names in manifest `access.events`. Envelope [internal/plugins/events.go:29-35]:
```json
{"event":"library.add","id":4182,"delivery":"<16 hex>","at":"RFC3339","payload":{}}
```
`id` omitted when 0. Overflow: `{"event":"missed","payload":{"after":<id>}}`. Up to 1000 missed
events are replayed after a restart. Valid names: [internal/plugins/manifest.go:116-124].
Payload [internal/alerts/webhook.go:35-56,149]:
```json
{"moment":"","event_kind":"","subject":"","text":"","at":"",
 "movie":{"id":1,"name":"","year":0,"tmdb":0}, "show":{"id":1,"name":"","year":0},
 "episodes":[{"season":1,"number":1,"name":""}], "data":{}}
```
- `library.add`: kind `title_added`.
- `request.made|approved|declined|available`: kinds `ask_made|ask_approved|ask_turned_down|ask_available`;
  `data` holds `ask_id`, `tmdb_id`, and `title_id` when there is no title object (value types
  unconfirmed) [internal/alerts/plugin_events.go:15-80,110-124]. People are never included.
stdin closing means the plugin is being stopped.

## Hook long-poll (used for pages only)
`GET /api/v1/plugin-hooks/<hook>` waits up to 25 s: `200 {"id","op","payload"}` or `204`.
`403` missing scope, `410` plugin stopped. Answer `POST` same path
`{"job":"<id>","data":…}` or `{"job":"<id>","error":"…"}` -> `204`. Must poll at least every 90 s.

## Pages (`pages.add`)
`PUT /api/v1/plugin-hooks/pages` body `[{"id":"^[a-z][a-z0-9-]{0,29}$","title":"","file":"index.html"}]`,
in memory only: re-PUT on every start. File job `op:"file"`, `payload:{"path":"<rel>"}`; reply
`data: {"status":200,"type":"text/html","body":"<base64>"}` within 10 s.
**Owner only** can open plugin pages. Sandbox has no `allow-popups`: external links do not open;
show the invite as selectable text [internal/plugins/hooks/pages.go:21-23,47-96].

## Home widget (`pages.home-widget`)
`PUT /api/v1/plugin-hooks/home-widget` body `{"title":"","rows":[{"text":"","sub":"","title_id":0,"link":""}]}`
-> `204`; `400` with the reason when refused; `403` without the scope. One card per plugin; a PUT
replaces it. Shown on Home to **every signed-in user** (`GET /api/v1/plugin-widgets`).
- Strict JSON: unknown fields are refused. `title` and each `text` required; `sub`, `title_id`,
  `link` optional. At most **20 rows**; each text at most **200 characters**; no `<` or `>`.
- `link` must match `^/[a-z0-9][a-z0-9/_\-?=&.]*$`, with no `..` and not `/api` or `/api/…`: a page
  inside HolyArrs such as `/settings/account` (My account). The row text becomes the link.
- In memory only, no expiry: kept until the plugin's token ends (stop or restart), so PUT on every
  start. There is no delete route; the card goes when the plugin stops or loses the scope.
  [internal/plugins/hooks/validate.go:14-27,46-112, routes.go:74,203-207,282-288, registry.go:31-41,57-93]

## Linking (`requests.users.read`)
- `POST /api/v1/plugin-hooks/link` `{"external":"<≤100 chars>"}` -> `{"code":"1234-5678","expires":"RFC3339"}`.
  10 minutes; a new call for the same external replaces the code.
- `GET /api/v1/plugin-hooks/users` -> `{"users":[{"id":1,"name":"login"}]}`.
- `X-Holyarrs-For-User: <id>`: accepted only on non-GET `/asks*`, `/ask-limits*`, `/turned-down*`
  and `*/follow` (any method), plus `GET /my/follows`. Unlinked -> `403 "that person has not linked
  this plugin to their account"`. On any other route the header is refused: `403 "a plugin acts
  for a person only on requests and follows"`.
- `GET /api/v1/plugin-hooks/links` -> `200 {"links":[{"user_id":7,"external":"302845117690216449","created_at":"RFC3339"}]}`.
  The calling plugin's own links, only for users who can still sign in, ordered by `user_id`.
  `external` is the text given to `POST …/link`. No links: `{"links":[]}`.
- `DELETE /api/v1/plugin-hooks/links/{user}` -> `200` with the remaining links in the same shape.
  Removes the calling plugin's link to that user id. A link that does not exist is not an error.
  `400 {"error":"name the person by their user id"}` for a bad id.
- Both link routes: `403` without the scope. Added in HolyArrs commit a73d48c8, after release
  1.3.1; an older HolyArrs has no such route (expect `404` or `405`), which the client maps to
  `core.ErrUnsupported`.
- A link is unique per (plugin, user): a user who types a second code replaces `external`. Two
  users can hold the same `external`.

## Data routes
A held scope covers its children with the same verb: `library.read` covers `library.artwork.read`
and `library.music.read` [internal/accounts/plugin_scopes.go, `scopeCovers`]. A route with no
plugin scope answers `403 "plugins can't use this route"`; a missing scope answers
`403 "this plugin was not given the <scope> permission"`.

| Scope | Route | Shape (trimmed) |
|---|---|---|
| library.read | `GET /api/v1/titles/{id}` | `{id,kind,name,year?,overview?,artwork:[str],added_at,size_bytes,asks:[…]}` [internal/discover/title_view.go:71-148] |
| library.read | `GET /api/v1/titles?q=&brief=1` | `{"titles":[{id,kind,name,artwork:[str]}]}` |
| library.read | `GET /api/v1/library/stats/watched` | `{enabled,read_at?,people:[{server,server_kind,name,linked,user_id?,user_name?,movies,shows,episodes,plays,last_watched_at?}]}` **per person totals, not per title** |
| library.read | `GET /api/v1/library/watched-cleanup?days=&people=k1,k2` | `{enabled,days,people:[{key,name,server,chosen}],found:[{id,kind,name,year?,bytes,last_watched_at}]}` |
| library.read | `GET /api/v1/titles/lookup?term=&year=` | movies; results carry tmdb id, `title_id?` (in library), `ask?` |
| library.read | `GET /api/v1/shows/lookup?term=&source=tmdb` | `{shows:[{…,title_id?,ask?}],source}` |
| library.artwork.read (held through library.read) | `GET /api/v1/artwork/{name}[?size=250]` | A stored picture, as listed in a title's `artwork`, a Home card's `poster` or a calendar date's `poster` (`/api/v1/artwork/<name>`). Raw bytes from disk: JPEG, PNG or WebP by the name's extension, at most 10 MB; `?size=250` gives a JPEG at most 250 px a side; `404` when not stored. The poster grid reads `?size=250` and caps at 1 MB; full size (capped at 5 MB) is read only for a new-title post. A `poster` that is not such a path is an outside `https` link (TMDB, TVmaze) the plugin cannot fetch |
| library.artwork.read | `GET /api/v1/artwork/tmdb/{size}/{file}`, `GET /api/v1/artwork/img/{kind}/{id}/{file}` | Pictures HolyArrs fetches on demand; a cache miss may 429 with `Retry-After: 60`. Not used by the bot |
| calendar.read | `GET /api/v1/calendar?from=YYYY-MM-DD&to=&titles=all` | `{dates:[{title_id,name,year?,kind,date,season?,episode?,episode_name?,poster?}]}` |
| discover.home.read | `GET /api/v1/home` | `{…,arrived:[…],soon:[{key,kind,title_id,name,year?,line?,poster?,at?}]}` (confirm `arrived` item shape in source) |
| requests.read | `GET /api/v1/asked-by?title_id=1&title_id=2` | `{"titles":{"<id>":{"askers":[{id,name,asked_at}]}}}`, max 200 ids |
| requests.read | `GET /api/v1/asks`, `GET /api/v1/asks/{id}` | Open to plugins, but useless: the list holds only the caller's own asks unless the caller may approve, and a plugin token is no person, so it is `{"asks":[],…}`; one ask by id is `404` for the same reason. With `X-Holyarrs-For-User` both answer `403` (a GET is not a for-person route) [internal/requests/routes_asks.go `listAsks`, internal/accounts/plugin_tokens.go `pluginForPerson`]. So a plugin cannot list a person's requests |
| requests.change + For-User | `POST /api/v1/asks` | `{"id":"tmdb:N","kind":"movie|show"}` -> 201/200 `{"ask":{id,kind,name,year?,poster?,state,title_id?},"joined","already"}`; 409 `{error,title_id,ready}` already in library; 429 + `Retry-After` `{error,retry_at,per_week,kind}` over the weekly limit [internal/requests/routes_asks.go:66-75,220-326] |
| library.delete | `POST /api/v1/titles/{id}/delete` | `{"delete_files":bool,"apply":bool,…}`; `apply:false` is a dry run |

There is no now-playing or sessions route.

## Manifest notes
`version` is required by the engine in dev mode. `*.discord.gg` matches `gateway.discord.gg` but
not `discord.gg`; list bare domains separately. Hosts limit is 100 in code. The manifest lists
only `discord.com` and `gateway.discord.gg`: discordgo 0.29.0 dials the address `GET /gateway`
returns (wsapi.go `Open`) for the first connection and for every reconnect and resume, and does
not read `resume_gateway_url`.
