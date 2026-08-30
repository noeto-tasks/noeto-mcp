# noeto-mcp

An MCP server that lets an AI agent work a [noeto](https://noeto.online) board:
read what is on it, create and update cards, move them between columns,
comment, and keep written documents attached to a card.

It runs over stdio and authenticates with a personal access token, so it needs
no browser and no cookie — which is the whole reason it exists.

## Setup

**1. Issue a token.** In noeto, Settings → Access tokens → Create token. Copy
the secret; it is shown once. The token is bound to the team you were in when
you created it.

**2a. As a plugin — the short way.** This repository is also a Claude Code
marketplace, and the plugin bundles the server config with the `/card` workflow:

```sh
/plugin marketplace add noeto-tasks/noeto-mcp
/plugin install noeto@noeto-mcp
```

Then export the token where Claude Code will see it — the plugin passes it
through from the environment rather than storing it — and restart:

```sh
export NOETO_TOKEN=noeto_pat_…
export NOETO_API_URL=https://api.noeto.online/api/v1
```

That is the whole install, and it registers the slash command too. See
[`plugins/noeto/README.md`](plugins/noeto/README.md). Everything below is the
same server registered by hand, which is what you want if you would rather run
the native binary, or run two teams side by side.

**2b. Point your agent at the image.** In Claude Code that is one command:

```sh
claude mcp add noeto -s user \
  -e NOETO_TOKEN=noeto_pat_… \
  -e NOETO_API_URL=https://api.noeto.online/api/v1 \
  -- docker run -i --rm -e NOETO_TOKEN -e NOETO_API_URL ghcr.io/noeto-tasks/noeto-mcp:v0.5.0
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
               "ghcr.io/noeto-tasks/noeto-mcp:v0.5.0"],
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
| `get_board` | one board: columns in order, which of them start and end it, cards nested in them | reads |
| `find_cards` | search every board — text, assignee, label, priority, column, due date, overdue; final columns left out | reads |
| `get_card` | one card in full: comment thread, and the files on it by name | reads |
| `list_members` | who is on the team | reads |
| `whoami` | which member the access token belongs to | reads |
| `create_card` | add a card to a column | writes |
| `update_card` | title, description, assignee, priority, due date, labels | writes |
| `move_card` | to another column, or reorder within one | writes |
| `comment_on_card` | post a comment | writes |
| `read_document` | the Markdown source of a document on a card | reads |
| `attach_document` | write one, replacing the previous of that name | replaces |
| `read_attachment` | any file on a card — text as text, an image as an image | reads |

**The last column is on the wire, not just in this table.** Each tool carries
the spec's annotations, so a host can tell the eight reads from the five writes
without being told and skip the approval prompt on the reads. The writes say
what they are too: none reaches past the one team the token belongs to, and
only `attach_document` is destructive — it deletes the copy it supersedes, so a
host can ask first. That matters because the absent-field defaults say the
opposite: a tool that ships no annotations is assumed destructive and
open-world.

**Names work where it is safe.** Boards, columns, labels, and people may be
given by name — `move_card(card, column: "Done")` rather than a UUID. The card
being changed may not: a wrong column is a visible mistake on the right card,
while a wrong card is a silent edit to work nobody was looking at. Ambiguous
names are an error listing the candidates, never a first-match guess.

**Clearing a field** is the word `none`: `update_card(card, due: "none")`. An
omitted argument leaves the field alone.

### Documents on a card

`attach_document` and `read_document` are a pair, and the pair is the point.

You pass Markdown and a filename, and that Markdown is the file.
`read_document` gives it back byte for byte, so the next pass over the card
builds on the last one instead of starting over. One artifact, no second copy to
drift.

The **filename is the identity**: it is what tells two documents on the same
card apart, what `read_document` asks for, and what a replace matches on.
Attaching `handover.md` beside a `design.md` leaves the latter untouched.
It defaults to `design.md`, which is the one the `/card` workflow keeps on
every card — the tools themselves are not about design documents specifically,
which is why they are not named for one.

That default use is worth describing, because it is what the shape is for. A
card records what was asked and a git history records what changed; neither
records *why this shape and not another one*, which is the expensive thing to
reconstruct a month later. So one attachment on the card carries it.

This used to be an HTML file with the Markdown sealed into a
`<script type="text/markdown">` block, so that a copy in a Downloads folder
would open typeset on a double click. It bought a print stylesheet at the price
of an encoding two repositories had to agree on forever, a rendered half that
could drift from the source it came from, and a reader that had to unseal a file
before it could show any of it. Markdown is legible unrendered, every editor and
diff tool already opens it, and the web app renders it on the card anyway.

Whatever the name, it is overwritten in place — no `design-v2.md`, because
after three rounds nobody can tell which one counts.
A replace is **upload, complete, then delete**, in that order: attachments have
no `PATCH` and the card has no unique constraint on the filename, so a duplicate
is briefly visible — and this way round the card holds two documents for a
moment and never zero. A failed upload deletes the row it reserved rather than
leaving a dead reservation behind.

It only ever deletes a copy under **the same name uploaded by the same
account**. A file somebody else uploaded is their work and is left alone, and so
is one whose uploader the API declined to name; anything left behind is named in
the answer so you know to clean it up. What it cannot tell apart any more is a
`design.md` you put there yourself through the web UI — the sealed source block
used to be that proof, and a plain `.md` has none. On the way back, if the card holds more than one file of the name,
`read_document` says so and names whose copy it returned — the newest wins, and that is not always
yours.

Both ends are bounded at 512 KB of Markdown, refused rather than truncated: a
silently shortened source would be written back as the whole document on the
next pass.

### Reading the other files

`read_attachment` is for everything the pair above did not write — a screenshot
pasted onto a bug, a log excerpt, an exported CSV. `get_card` names what is on
a card; this reads one of them by that name.

**Text comes back as text and an image as an image.** Anything else — a PDF, an
archive, a binary — is refused by name and by size, because there is no useful
way to put it in front of a model and a refusal that says so beats base64 the
model throws away.

**The declared content type decides nothing on its own.** It is a field the
uploader filled in at upload time, not something the API measured, so it only
routes the attempt and the bytes overrule it: text has to decode as UTF-8 and
carry no control characters, and an image is re-sniffed and sent under the type
its bytes actually are. A binary calling itself `text/plain` is refused rather
than poured into the conversation. SVG is the exception among images — it is
markup that can carry script and no decoder is involved in reading it, so its
source comes back as text.

**A document `attach_document` wrote answers with its Markdown source here
too**, so the two tools never give different answers about the same file.

Limits are 512 KB for text and 1.5 MB for an image, checked against the length
the API signed into the upload — so an oversized file is refused before it is
fetched rather than after. And as with the document pair, the presigned URL
never leaves the process: contents come back, links never do.

## What it deliberately does not do

- **Create boards, columns, or labels.** Setting a board up is a different job
  from working one, and the schemas would cost every conversation context it
  will not use.
- **Delete anything a person made.** A card in the wrong column is recoverable;
  a deleted one is not. The only delete is `attach_document` removing the
  previous copy of a document it wrote itself.
- **Upload arbitrary files.** It is a three-request presigned dance, and the
  one file worth writing from here is the document `attach_document` writes.
  Reading is a different matter and `read_attachment` does it — but like the
  document pair, it fetches inside the process and answers with contents,
  because a download link is a short-lived bearer credential a model must never
  be handed.

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
