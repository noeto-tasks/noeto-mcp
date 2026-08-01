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

**2. Point your agent at the image.** In Claude Code, `~/.claude.json` or the
project's `.mcp.json`:

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
container goes when the conversation does. The two `-e` flags name the
variables without values, so the token stays in one place — the `env` block —
rather than being repeated on a command line that shows up in `ps`.

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

If you have a Go toolchain, `make install` puts `noeto-mcp` in your `GOBIN` and
the config becomes `"command": "/absolute/path/to/noeto-mcp"` with the same
`env` block. Use an absolute path — the agent host's working directory is not
yours.

### What this does not reach

A stdio server runs on the machine the agent runs on, so this covers Claude
Code and the desktop app. **claude.ai in a browser and the mobile apps cannot
start a local process at all**, whatever it is packaged as. Reaching those
needs a remote MCP server with OAuth, which is a different transport and a
different authentication story — not a packaging problem.

## Tools

| tool | what it does |
|---|---|
| `list_boards` | the team's boards, with card counts |
| `get_board` | one board: columns in order, cards nested in them |
| `find_cards` | search every board — text, assignee, label, priority, column, due date, overdue |
| `get_card` | one card in full, with its comment thread |
| `list_members` | who is on the team |
| `create_card` | add a card to a column |
| `update_card` | title, description, assignee, priority, due date, labels |
| `move_card` | to another column, or reorder within one |
| `comment_on_card` | post a comment |

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
```

`make docker-push` needs `docker login ghcr.io` with a token carrying
`write:packages`, and the package has to be made public once before anyone can
pull it anonymously.

`make smoke` earns its place because this repo is separate from `noeto-api`:
that repo's `make openapi-check` cannot see this client, so nothing else would
notice the API renaming a field until an agent got an empty board. The unit
tests run against a fake whose shapes are a copy of the API's — a fake agrees
with itself forever. Run the smoke test after any API change.
