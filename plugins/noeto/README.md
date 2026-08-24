# noeto plugin

The noeto board inside Claude Code: the MCP server and the workflow that drives
it, installed together.

```sh
/plugin marketplace add noeto-tasks/noeto-mcp
/plugin install noeto@noeto-mcp
```

That registers the MCP server *and* the `/card` command. There is no
`claude mcp add` step and nothing to paste into a config file.

## Before it works: the token

The server authenticates with a personal access token, and the plugin passes it
through from your environment rather than storing it. Issue one in noeto under
**Settings → Access tokens**, then put it in your shell profile:

```sh
export NOETO_TOKEN=noeto_pat_…
export NOETO_API_URL=https://api.noeto.online/api/v1
```

Restart Claude Code afterwards, so the server starts with the variables set.

Keeping the token in the environment rather than in the plugin is deliberate: a
value written into a config file is a secret sitting in a file that gets synced,
copied and screenshotted. The token is bound to one team, so one export serves
one team — a second team needs a second token and its own server entry, which is
the point where you register that one by hand.

**It needs Docker.** The plugin runs the server as `docker run -i --rm`, which
is the one form that needs no Go toolchain, no PATH assumptions and no
unquarantining on macOS. If you would rather run the native binary — `brew
install noeto-tasks/tap/noeto-mcp` — disable this plugin's server and register
that one yourself; the [repository README](../../README.md) has the command.

> **Pointing at a local noeto?** Set `NOETO_API_URL` anyway — do not leave it
> out. The default is `http://localhost:8081/api/v1`, and the plugin runs the
> server in a container, where localhost *is* the container. Use
> `http://host.docker.internal:8081/api/v1` on macOS and Windows. On Linux that
> host does not exist, so register the server by hand with `--network host`
> instead. The server detects this case and says so in the error, but it is
> easier to get right the first time.

## What you get

**Eleven MCP tools** — read the board, create and update cards, move them,
comment, and keep a written document on a card. The
[repository README](../../README.md) documents the whole surface.

**Formatted card lists, without typing a command.** Ask in plain language —
"my cards", "what is overdue", "what is Jana waiting on" — and the answer comes
back as a table with priority, who asked for the card, its comment count and its
due date, rather than as raw tool output. That is a model-invoked skill, so
there is nothing to remember.

One thing it will ask you once: **which team member you are.** A personal access
token cannot ask noeto who it belongs to — `/me` is `AuthUser` and tokens are
confined to `AuthTenant` — so "my cards" has no way to resolve itself. Tell it
once and it holds on to the answer.

**`/card`** — takes one card to implemented work and reports back onto it:

```
/card                    pick from the cards not yet in a last column
/card <card-id>          that card
/card limit for parents  find it by text
```

It is an **adapter, not an implementer**. Its one decision is triage: is this
card buildable as written? Any contradiction between the description and the
comment thread means it asks on the card and reassigns, rather than building a
confident misunderstanding. When the card is clear, it writes a design document
onto the card, hands the implementation to whatever implementation command you
use, and reports the result back — comment, commit sha, one column to the right.

Run it from the directory whose subdirectories are your repositories; that
listing is what it offers as implementation targets.

### It delegates, so it expects company

`/card` deliberately does not implement or commit. It looks for a `/feature`
command to hand the work to, and leaves the commit to you. Without one it will
stop after writing the design document and hand you the requirement — which is
the correct behaviour for an adapter, but means the plugin is most useful
alongside whatever implementation and commit workflow you already have.

## The design document

The `/card` flow keeps one attachment on each card, `design.html`, and reads it
back on the next pass. That round trip is the point: a card records what was
asked and a git history records what changed, but neither records *why this
shape and not another one* — which is exactly what is expensive to reconstruct a
month later.

You write Markdown; `attach_document` typesets it into one self-contained HTML
file and seals the source inside it, so `read_document` returns what you wrote,
byte for byte. It is downloaded rather than previewed — noeto only previews
images inline — and a downloaded `.html` opens typeset on a double click, which
a `.md` does not.

The tools are not specific to design documents: the filename is a parameter, and
`design.html` is only the default. Attaching `handover.html` beside it leaves the
design record untouched.
