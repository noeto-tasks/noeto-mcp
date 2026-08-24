---
name: card-lists
description: How to present noeto cards to a person. Use whenever the answer is a list of cards — "my cards", "what am I working on", "what is on the board", "what is overdue", "what is assigned to X", "what should I do next" — and whenever a noeto card list is about to be shown for any other reason. Covers resolving who "me" is, which single call to make, and the table to render.
version: 1.0.0
---

# Showing noeto cards

A list of cards is something a person scans, not something they read. The
default failure is dumping the tool's JSON, or writing a paragraph per card;
both make the reader do the work of finding the one card that matters.

## One call, not N

`find_cards` already returns everything below — title, assignee, creator,
priority, due date, comment count, attachment count, labels, board, column.
**Do not call `get_card` per row to fill the table in.** Reach for `get_card`
only when the person asks about one specific card and wants its description or
thread.

Filter server-side rather than fetching everything and filtering in the answer:
`assignee`, `label`, `priority`, `column`, `board`, `due_before`, `due_after`,
`overdue`, `text`. Results come back soonest-due first, undated last; keep that
order unless asked otherwise, because the top of the list is the answer to
"what should I do next".

## "My cards"

**A personal access token cannot ask noeto who it belongs to.** The `/me`
endpoint is `AuthUser` and tokens are confined to `AuthTenant`, so there is no
"assigned to me" filter and no way to derive one. This is a hard limit, not a
gap to work around.

So when somebody says "my cards" and you do not already know which team member
they are:

1. Call `list_members`.
2. Ask them which one they are — once.
3. Remember it for the rest of the session, and offer to record it so it does
   not have to be asked again.

Never guess from a name that looks similar to the account you are running under.
Assigning or filtering against the wrong person is a silent wrong answer.

## The table

Render as a Markdown table, in the language the person is speaking:

| ! | card | column | asked by | 💬 | due | id |
|---|------|--------|----------|----|-----|----|
| 🔴 | Maximální počet dětí u rodiče | Rozpracované | Jana Nováková | 3 | ⚠️ 20. 8. | `5a81e9c5` |
| 🟡 | Export objednávek do CSV | Rozpracované | Michal Bocek | 0 | 29. 8. | `8c02fd11` |
| ⚪ | Oprava přihlášení na Safari | Ověřit | Eva Horáková | 5 | — | `2f9a4b70` |

- **`!`** is priority: 🔴 high, 🟡 medium, ⚪ low, blank when unset.
- **asked by** is `created_by` — who wants the work, which on a board of
  incoming requests is who you go back to when something is unclear. Use
  `assignee` in that column instead **only** when the list is somebody else's
  cards, or spans several people, so it says who is doing it. Never show both
  columns; pick the one the question is about.
- **💬** is the comment count. A card with a thread is a card whose description
  is probably not the whole requirement. Show `📎 n` beside it only when
  attachments exist.
- **due** is ⚠️ plus the date when overdue, the plain date otherwise, `—` when
  unset. Write dates the short way the person's language uses, not ISO.
- **id** is the first 8 characters of the card id. It is what `/card <id>`
  takes and what goes in a commit message as `(noeto: <id>)`, so the list stays
  actionable. Drop this column only if asked to.

Add a **board** column when the results span more than one board; leave it out
when they do not, since repeating one value down a column is noise.

## Around the table

- Lead with one line saying what was asked and how many came back — "6 cards
  assigned to Michal, 2 overdue" — so the count does not have to be counted.
- When something in the list is genuinely urgent, say it in a sentence under
  the table rather than relying on the reader spotting ⚠️.
- **Empty is a real answer.** "Nothing assigned to you outside the last column"
  is useful and complete; do not pad it, and do not silently widen the filter to
  find something to show.
- Long titles get truncated with `…` rather than wrapping the table.
- Never print the raw tool output alongside the table.

## When a list is the wrong shape

- **One card** asked about specifically → prose, not a one-row table. Title,
  who asked, what it says, what the thread added.
- **More than about 25 rows** → say how many there are and show the top of the
  list, then offer a narrower filter. A table nobody scrolls is not an answer.
- **A whole board** asked about → `get_board` nests cards inside columns
  already; a column-by-column list reads better than a flat table there.
