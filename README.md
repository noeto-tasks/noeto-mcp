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

**2. Install.**

```sh
make install
```

**3. Point your agent at it.** In Claude Code, `~/.claude.json` or the project's
`.mcp.json`:

```json
{
  "mcpServers": {
    "noeto": {
      "command": "/Users/you/go/bin/noeto-mcp",
      "env": {
        "NOETO_TOKEN": "noeto_pat_…",
        "NOETO_API_URL": "https://api.noeto.online/api/v1"
      }
    }
  }
}
```

`NOETO_API_URL` defaults to `http://localhost:8081/api/v1`, which is what
`noeto-local` serves. Use an absolute path for `command` — the agent host's
working directory is not yours.

**One process serves one team.** The token says which. Two teams means two
entries, each with its own token, named apart (`noeto-work`, `noeto-personal`).

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
make test    # hermetic — runs against a fake API
make smoke   # contract check against a running noeto (needs NOETO_TOKEN)
make lint
```

`make smoke` earns its place because this repo is separate from `noeto-api`:
that repo's `make openapi-check` cannot see this client, so nothing else would
notice the API renaming a field until an agent got an empty board. The unit
tests run against a fake whose shapes are a copy of the API's — a fake agrees
with itself forever. Run the smoke test after any API change.
