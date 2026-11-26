# Changelog

All notable changes to this plugin are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Tags are plain `x.y.z`, with no `v`.

## [Unreleased]

### Changed

- A grid tile that leaves out a stored poster now logs why (a reason and size, never a name).

## [1.0.0] - 2026-11-26

First release. Needs HolyArrs 1.3.2 or newer.

### Added

- Linking: `/link` gives a private one-time code to type in HolyArrs under My account, Linked
  plugins. The bot notices within a few seconds and messages the member. `/unlink` and `/whoami`.
- Home card: a "Discord" card on the HolyArrs Home page with the invite link, how to link, and
  how many members have linked.
- Dashboard: four self-updating pinned tiles: Recently added, Recently watched, Top watchers and
  Coming up. Recently added and Coming up show a 3x3 grid of posters with a numbered list under
  it.
- Announcements: a post when a title is added, and a DM when a title you asked for is available.
  A request made through the bot also gets a DM when it is approved or declined.
- Requests: `/search`, `/request` and `/status`. Requests are made as the linked person, under
  their own limits.
- Privacy: names on Top watchers are opt-in with `/privacy show_watching:on`.
  `/privacy alerts:off` turns every request DM off: approved, declined and available.
- Admin commands: `/dashboard refresh`, `/link-admin list` and `/link-admin remove`.
- Optional Linked role, given to members once they link.
- A read-only self-check on every start that logs one line per dependency, with counts only.

- Security: two hosts only (`discord.com` and `gateway.discord.gg`), no red-warning permission,
  and logs that hold counts only: no ids, names, titles or invite link.

[1.0.0]: https://github.com/NotCavnfox/holyarrs-discord/releases/tag/1.0.0
