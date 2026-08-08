# noeto-mcp

An MCP server that lets an AI agent work a [noeto](https://noeto.online) board:
read what is on it, create and update cards, move them between columns, and
comment.

It runs over stdio and authenticates with a personal access token, so it needs
no browser and no cookie — which is the whole reason it exists.

## Setup

**1. Issue a token.** In noeto, Settings → Access tokens → Create token. Copy
the secret; it is shown once. The token is bound to the team you were in when
you created it.

**2. Point your agent at the image.** In Claude Code that is one command:

```sh
claude mcp add noeto -s user \
  -e NOETO_TOKEN=noeto_pat_… \
  -e NOETO_API_URL=https://api.noeto.online/api/v1 \
  -- docker run -i --rm -e NOETO_TOKEN -e NOETO_API_URL ghcr.io/noeto-tasks/noeto-mcp:latest
```

`-s user` registers the server for every project instead of just the current
directory, which is what you want for a tool that follows your work around; the
default is the directory you happen to be standing in. Everything after `--` is
the command the agent host will run.

The same thing by hand — for another host, or to check what the command wrote —
in `~/.claude.json` or the project's `.mcp.json`:

```json
{
  "mcpServers": {
    "noeto": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "NOETO_TOKEN", "-e", "NOETO_API_URL",
               "ghcr.io/noeto-tasks/noeto-mcp:latest"],
      "env": {
        "NOETO_TOKEN": "noeto_pat_…",
        "NOETO_API_URL": "https://api.noeto.online/api/v1"
      }
    }
  }
}
```

That is the whole install: no Go, no Node, and nothing to unquarantine. `-i`
keeps stdin open, which is the pipe the protocol rides; `--rm` means the
container goes when the conversation does. The two `-e` flags after `docker run`
name the variables without values, so the token stays in the `env` block rather
than riding a command line that shows up in `ps` every time the server starts.

(It does pass through your own shell once, in the `claude mcp add` line, which
most shells write to history. If that bothers you, edit the JSON instead.)

The cost is that Docker has to be installed and running, and each session pays
about a second to start the container. For a server the host starts once per
conversation, that is not a latency anyone notices.

> **Pointing at a local noeto?** `localhost` inside a container is the
> container. Use `http://host.docker.internal:8081/api/v1` on macOS and
> Windows, or add `--network host` on Linux. The server detects this case and
> says so in the error, but it is easier to get right the first time.

**One process serves one team.** The token says which. Two teams means two
entries, each with its own token, named apart (`noeto-work`, `noeto-personal`).

### Without Docker

It is a single static binary, so there is nothing to install alongside it.
With Homebrew:

```sh
brew install noeto-tasks/tap/noeto-mcp
claude mcp add noeto -s user \
  -e NOETO_TOKEN=noeto_pat_… \
  -e NOETO_API_URL=https://api.noeto.online/api/v1 \
  -- "$(brew --prefix)/bin/noeto-mcp"
```

Without Homebrew, take the archive for your platform from the
[releases page](https://github.com/noeto-tasks/noeto-mcp/releases) — macOS,
Linux and Windows, x86 and arm — unpack it, and point the config at wherever
you put it. On macOS the download arrives quarantined, because the binary is
not signed; `xattr -d com.apple.quarantine noeto-mcp` clears it. The cask does
that for you, which is the reason to prefer it.

And with a Go toolchain, `make install` puts `noeto-mcp` in your `GOBIN` and
prints where it landed:

```sh
make install     # ==> /Users/you/go/bin/noeto-mcp
```

By hand, all three are `"command": "/absolute/path/to/noeto-mcp"` with the same
`env` block. Use an absolute path — the agent host's working directory is not
yours.

### What this does not reach

A stdio server runs on the machine the agent runs on, so this covers Claude
Code and the desktop app. **claude.ai in a browser and the mobile apps cannot
start a local process at all**, whatever it is packaged as. Reaching those
needs a remote MCP server with OAuth, which is a different transport and a
different authentication story — not a packaging problem.

## Tools

| tool | what it does | |
|---|---|---|
| `list_boards` | the team's boards, with card counts | reads |
| `get_board` | one board: columns in order, cards nested in them | reads |
| `find_cards` | search every board — text, assignee, label, priority, column, due date, overdue | reads |
| `get_card` | one card in full, with its comment thread | reads |
| `list_members` | who is on the team | reads |
| `create_card` | add a card to a column | writes |
| `update_card` | title, description, assignee, priority, due date, labels | writes |
| `move_card` | to another column, or reorder within one | writes |
| `comment_on_card` | post a comment | writes |

**The last column is on the wire, not just in this table.** Each tool carries
the spec's annotations, so a host can tell the five reads from the four writes
without being told and skip the approval prompt on the reads. The writes say
what they are too: none of them is destructive — nothing here deletes — and
none reaches past the one team the token belongs to. That matters because the
absent-field defaults say the opposite: a tool that ships no annotations is
assumed destructive and open-world.

**Names work where it is safe.** Boards, columns, labels, and people may be
given by name — `move_card(card, column: "Done")` rather than a UUID. The card
being changed may not: a wrong column is a visible mistake on the right card,
while a wrong card is a silent edit to work nobody was looking at. Ambiguous
names are an error listing the candidates, never a first-match guess.

**Clearing a field** is the word `none`: `update_card(card, due: "none")`. An
omitted argument leaves the field alone.

## What it deliberately does not do

- **Create boards, columns, or labels.** Setting a board up is a different job
  from working one, and the schemas would cost every conversation context it
  will not use.
- **Delete anything.** A card in the wrong column is recoverable; a deleted one
  is not.
- **Attachments.** Uploading is a three-request presigned dance, and download
  links are short-lived credentials a model must not repeat.

## Development

```sh
make test         # hermetic — runs against a fake API
make smoke        # contract check against a running noeto (needs NOETO_TOKEN)
make lint
make docker       # build the image for this machine
make docker-push  # build and push amd64 + arm64 to GHCR
make release-dry  # build the release artifacts into dist/, publish nothing
make release      # publish a tagged release + update the Homebrew tap
```

`make docker-push` needs `docker login ghcr.io` with a token carrying
`write:packages`, and the package has to be made public once before anyone can
pull it anonymously.

`make release` needs a tag on HEAD, a clean tree, and `GITHUB_TOKEN` with
`repo` scope — a classic token, since it writes a release here and commits the
cask to the tap, and the same one can carry `write:packages` for the image.
Tokens can go in a gitignored `.env` rather than into your shell history —
`cp .env.example .env` and fill in what you need; `make` reads it if it is
there, and every target that wants one still says so when it is missing.

There is no CI in this repo, so cutting a version is three commands from a
laptop and they are easy to get out of order:

```sh
git tag v0.1.0 && git push --tags
make release
make docker-push   # the same tag now names the image, not a commit sha
```

`make release-dry` runs the whole thing into `dist/` without publishing, which
is the way to find out that an archive is malformed before a stranger does.

`make smoke` earns its place because this repo is separate from `noeto-api`:
that repo's `make openapi-check` cannot see this client, so nothing else would
notice the API renaming a field until an agent got an empty board. The unit
tests run against a fake whose shapes are a copy of the API's — a fake agrees
with itself forever. Run the smoke test after any API change.
