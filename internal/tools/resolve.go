package tools

import (
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/rotisserie/eris"

	"noeto-mcp/internal/noeto"
)

// Resolving what the model typed into what the API needs.
//
// A model working from a board snapshot has the names in front of it — "Done",
// "Michal", "bug" — and would have to scroll back to find the matching UUID.
// Making it do that wastes a turn and gets transposed digits wrong. So the
// write tools accept either form and resolve here.
//
// The line this draws matters: **targets** may be named, the **subject** may
// not. Moving a card accepts a column called "Done", but the card being moved
// must be given by id. A wrong column is a visible mistake on the right card;
// a wrong card is a silent edit to work nobody was looking at. Names are
// ambiguous by nature, so they are allowed only where being wrong is cheap.

// isID reports whether s is a UUID rather than a name.
func isID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// requireID rejects a name where an id is required, naming the tool that
// produces one — a model told only "invalid" will guess again rather than go
// and look it up.
func requireID(kind, value string) error {
	if isID(value) {
		return nil
	}
	return eris.Errorf(
		"%s must be given by id, not by name (%q) — ids come from get_board or find_cards",
		kind, value)
}

// normalize reduces a name to the letters and digits in it, lowercased.
//
// Because "Todo" and "To Do" are the same word, and a model asked to move
// something to the To Do column will write it either way — as will a person
// typing an instruction. Before this, "Todo" missed and the model spent a turn
// reading back an error to discover the space. Punctuation goes the same way,
// so "in-progress" reaches "In Progress".
//
// This is not fuzziness: it removes characters that carry no meaning in a
// column or label name. Two names that differ only in spacing were never
// distinguishable to the person reading them either.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// match finds the one candidate a name refers to.
//
// Exact match wins outright; only if nothing matches exactly does it fall back
// to a substring search. Without that precedence a board with "Done" and "Not
// done" would make "Done" ambiguous, which is absurd to a person looking at it.
//
// Ambiguity is an error rather than a first-match guess. Picking one silently
// is how an agent moves the right card to the wrong place and reports success.
func match(kind, want string, candidates map[string]string) (string, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return "", eris.Errorf("no %s given", kind)
	}
	if isID(want) {
		return want, nil
	}

	needle := normalize(want)
	var exact, partial []string
	for id, name := range candidates {
		name := normalize(name)
		switch {
		case name == needle:
			exact = append(exact, id)
		case strings.Contains(name, needle):
			partial = append(partial, id)
		}
	}

	hits := exact
	if len(hits) == 0 {
		hits = partial
	}

	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", eris.Errorf("no %s called %q — available: %s", kind, want, listNames(candidates))
	default:
		return "", eris.Errorf("%q matches more than one %s (%s) — name it exactly, or use its id",
			want, kind, strings.Join(namesOf(hits, candidates), ", "))
	}
}

func namesOf(ids []string, candidates map[string]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, candidates[id])
	}
	sort.Strings(out)
	return out
}

func listNames(candidates map[string]string) string {
	if len(candidates) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(candidates))
	for _, name := range candidates {
		out = append(out, name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func boardIndex(list []noeto.Board) map[string]string {
	index := make(map[string]string, len(list))
	for _, b := range list {
		index[b.ID] = b.Name
	}
	return index
}

func columnIndex(list []noeto.Column) map[string]string {
	index := make(map[string]string, len(list))
	for _, c := range list {
		index[c.ID] = c.Name
	}
	return index
}

func labelIndex(list []noeto.Label) map[string]string {
	index := make(map[string]string, len(list))
	for _, l := range list {
		index[l.ID] = l.Name
	}
	return index
}

// memberIndex keys on both name and email, because a model that has seen a
// board knows assignees by display name while a person instructing it is as
// likely to paste an address.
func memberIndex(list []noeto.Member) map[string]string {
	index := make(map[string]string, len(list)*2)
	for _, m := range list {
		index[m.UserID] = m.Name
	}
	return index
}

// matchMember resolves a person by id, display name, or email address.
func matchMember(want string, list []noeto.Member) (string, error) {
	trimmed := strings.TrimSpace(want)
	if isID(trimmed) {
		return trimmed, nil
	}
	// Email is unique and exact, so try it before falling back to the fuzzy
	// name match — "michal@…" should never be ambiguous.
	for _, m := range list {
		if strings.EqualFold(m.Email, trimmed) {
			return m.UserID, nil
		}
	}
	return match("team member", trimmed, memberIndex(list))
}
