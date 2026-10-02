# How noeto-mcp behaves, and why

The README says what the server does. This is the reasoning behind the parts
that are not obvious from the outside.

## Tool annotations

Each tool carries the MCP spec's annotations, so a host can tell the reads from
the writes and the one save to disk without being told, and skip the approval
prompt on the reads. None of the writes reaches past the one team the token
belongs to, and only `attach_document` is destructive — it deletes the copy it
supersedes, so a host can ask first. That matters because the absent-field
defaults say the opposite: a tool that ships no annotations is assumed
destructive and open-world.

## Names, not ids — except for the card

Boards, columns, labels and people may be given by name. The card being changed
may not: a wrong column is a visible mistake on the right card, while a wrong
card is a silent edit to work nobody was looking at. Ambiguous names are an
error listing the candidates, never a first-match guess.

## Documents on a card

A card records what was asked and a git history records what changed; neither
records *why this shape and not another one*, which is the expensive thing to
reconstruct a month later. `attach_document` and `read_document` keep that on
the card as one Markdown file, returned byte for byte, so the next pass builds
on the last one instead of starting over.

The **filename is the identity**: it tells two documents on one card apart and
is what a replace matches on. It is overwritten in place — no `design-v2.md`,
because after three rounds nobody can tell which one counts.

A replace is **upload, complete, then delete**, in that order. Attachments have
no `PATCH` and the card has no unique constraint on the filename, so a duplicate
is briefly visible — and this way round the card holds two documents for a
moment and never zero. A failed upload deletes the row it reserved.

It only deletes a copy under **the same name uploaded by the same account**. A
file somebody else uploaded is left alone, and so is one whose uploader the API
declined to name; anything left behind is named in the answer. What it cannot
tell apart is a `design.md` you uploaded yourself through the web UI. When a
card holds more than one file of the name, `read_document` says so and names
whose copy it returned — the newest wins, and that is not always yours.

Both ends are capped at 512 KB, refused rather than truncated: a silently
shortened source would be written back as the whole document on the next pass.

The format used to be HTML with the Markdown sealed into a
`<script type="text/markdown">` block, so a downloaded copy opened typeset. It
cost an encoding two repositories had to agree on forever and a rendered half
that could drift from its source. Markdown is legible unrendered, and the web
app renders it on the card anyway.

## Reading other files

**The declared content type decides nothing on its own.** The uploader filled it
in; the API never measured it. So it only routes the attempt and the bytes
overrule it: text has to decode as UTF-8 with no control characters, and an
image is re-sniffed and sent under the type its bytes actually are. SVG is
markup that can carry script, so its source comes back as text.

Anything else — a PDF, an archive — is refused by name and size, because there
is no useful way to put it in front of a model, and a refusal that says so beats
base64 the model throws away. Limits are checked against the length the API
signed into the upload, so an oversized file is refused before it is fetched.

## Presigned URLs never leave the process

Every file tool fetches inside the server and answers with contents or a local
path. A presigned URL is a bearer credential a model must never be handed —
which is also why the server does not upload arbitrary files.

## Downloads

The filename is the uploader's, so it is stripped of any directory part and of
control characters before it touches the disk. Saving the same file again
replaces the earlier copy.

## What it deliberately does not do

- **Create boards, columns or labels.** Setting a board up is a different job
  from working one, and those schemas would cost every conversation context it
  will not use.
- **Delete anything a person made.** A card in the wrong column is recoverable;
  a deleted one is not.

## Why `make smoke` exists

This repo is separate from `noeto-api`, so that repo's `make openapi-check`
cannot see this client. The unit tests run against a fake whose shapes are a
copy of the API's — and a fake agrees with itself forever. The smoke test is
what notices the API renaming a field before an agent gets an empty board.
