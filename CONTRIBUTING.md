# Contributing

Thanks for wanting to help. This page gets you from a fresh clone to a pull request.

This is a small project with one maintainer. Every issue and pull request is read by hand, so a
reply can take a few days.

## How it works

1. For anything bigger than a small fix (a typo, a broken link, a one-line bug), open an issue
   first, or comment on one that exists. Say what you want to change and why.
2. Work on a branch, never on `main`. Fork the repo if you can't push here.
3. Open a pull request and link the issue (`Closes #12` or `Refs #12`).
4. One fix or one feature per pull request. Small ones get merged faster.

Security problems do not go in issues. See [SECURITY.md](SECURITY.md).

## Build and test

You need Go, the version in `go.mod`. Nothing else.

    git clone https://github.com/NotCavnfox/holyarrs-discord.git
    cd holyarrs-discord
    go vet ./...
    go test -race ./...

The tests never touch the network. They use in-memory fakes and `httptest`.

HolyArrs builds the plugin from source for three CPU types. Make sure all three build the same
way:

    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -mod=readonly -o build/amd64/plugin .
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -mod=readonly -o build/arm64/plugin .
    CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -mod=readonly -o build/armv7/plugin .

Format with `gofmt`. CI runs all of the above on every pull request, in three checks: `test`,
`build` and `manifest`. `manifest` runs the HolyArrs plugin catalogue's own check on
`holyarrs-plugin.json`.

## Try it in HolyArrs

Use HolyArrs developer mode on your own server:

1. Build the binary for your server's CPU type (see above).
2. Copy `holyarrs-plugin.json` and the built `plugin` file into
   `<config folder>/plugins/_dev/discord/`.
3. On the Plugins page, choose Install from folder.

Use a test Discord server and a test bot token. Never a live one.

## Where the code lives

[ARCHITECTURE.md](ARCHITECTURE.md) explains the layout. Read it before a bigger change. The
HolyArrs wire shapes are in [docs/holyarrs-api.md](docs/holyarrs-api.md).

## Plugin rules

The plugin runs in a sandbox, and owners approve what it may do when they install it. A change
must keep all of these:

- Pure Go. No cgo, no prebuilt programs, no code downloaded at run time.
- Call only the hosts listed in `access.hosts` in `holyarrs-plugin.json`.
- Use only the HolyArrs permissions listed in `access.actions`.
- Write only inside the plugin data folder (`HOLYARRS_PLUGIN_DATA`).
- No UDP and no listening for connections.
- Nothing secret in logs or on disk: no bot token, no link codes.
- No names, titles or ids in logs. Log counts and reasons only.

A new host or a new permission is a big deal: owners see it at install and have to approve it.
If your change needs one, say so at the top of the pull request and explain why. Expect questions.

## Wording

Text that people see (command replies, tiles, settings, the README) is plain and short. Short
sentences, common words, no jargon. Say what happened and what to do next. Leave existing wording
alone unless the issue is about wording.

## Pull requests

- Link the issue.
- Say what changed and how you tested it.
- Add or update tests for what you changed.
- Update the README if users see the change, and add a line to `CHANGELOG.md` under the next
  version.
- Write comments that explain why the code does what it does.

There is no contributor licence agreement. Your contribution is under the project's
[MIT licence](LICENSE).

## Releases

The maintainer does these. You don't need to.

1. Update `CHANGELOG.md` and `whats_new` in `holyarrs-plugin.json`.
2. Tag the commit `x.y.z`, with no `v` (for example `1.0.1`), and push the tag. A GitHub Release
   is made from that version's section of the changelog. It holds source only, because HolyArrs
   builds the plugin itself.
3. Open a pull request on
   [HolyArrs/HolyArrs-Plugins](https://github.com/HolyArrs/HolyArrs-Plugins) that changes `tag`
   and `commit` in `catalog/active/discord.json`.

A published tag never moves. A fix always gets a new version.

## Code of conduct

Be kind. Read the [code of conduct](CODE_OF_CONDUCT.md).
