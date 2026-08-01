package tools

import (
	"strings"
	"testing"

	"noeto-mcp/internal/noeto"
)

// Exact match must beat substring, or a board with "Done" and "Not done" makes
// the word "Done" ambiguous — which is absurd to anyone looking at the board.
func TestMatch_ExactBeatsSubstring(t *testing.T) {
	columns := map[string]string{"a": "Done", "b": "Not done"}

	got, err := match("column", "Done", columns)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if got != "a" {
		t.Fatalf("Done resolved to %q, want the exactly-named column", got)
	}
}

func TestMatch_SubstringWhenNothingIsExact(t *testing.T) {
	got, err := match("column", "prog", map[string]string{"a": "In Progress", "b": "Done"})
	if err != nil {
		t.Fatalf("prog: %v", err)
	}
	if got != "a" {
		t.Fatalf("got %q, want a", got)
	}
}

func TestMatch_IsCaseInsensitive(t *testing.T) {
	if _, err := match("column", "IN PROGRESS", map[string]string{"a": "In Progress"}); err != nil {
		t.Fatalf("uppercase: %v", err)
	}
}

// Ambiguity is an error, not a first-match guess. Silently picking one is how
// an agent edits the wrong thing and reports success.
// "rev" and not "review": the latter matches "Review" exactly, and exact
// beating substring is the behaviour the test above pins down.
func TestMatch_AmbiguousIsAnError(t *testing.T) {
	_, err := match("column", "rev", map[string]string{"a": "Review", "b": "Peer review"})
	if err == nil {
		t.Fatal("expected an error for an ambiguous name")
	}
	// The message has to carry the candidates, or the model cannot pick.
	for _, want := range []string{"Review", "Peer review"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

// A miss must list what IS available — a bare "not found" costs the model a
// turn re-reading the board it already has.
func TestMatch_UnknownNameListsTheOptions(t *testing.T) {
	_, err := match("column", "Shipped", map[string]string{"a": "Todo", "b": "Done"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Todo") || !strings.Contains(err.Error(), "Done") {
		t.Errorf("error should list the available columns: %v", err)
	}
}

func TestMatch_UUIDPassesThroughUnchecked(t *testing.T) {
	const id = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	got, err := match("column", id, map[string]string{"other": "Todo"})
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if got != id {
		t.Fatalf("got %q, want the id unchanged", got)
	}
}

// Email is unique and exact, so it must not go through the fuzzy name path.
func TestMatchMember_ResolvesByEmail(t *testing.T) {
	members := []noeto.Member{
		{UserID: "u1", Name: "Michal Bocek", Email: "michal@example.com"},
		{UserID: "u2", Name: "Michaela Novak", Email: "michaela@example.com"},
	}

	got, err := matchMember("michal@example.com", members)
	if err != nil {
		t.Fatalf("email: %v", err)
	}
	if got != "u1" {
		t.Fatalf("got %q, want u1", got)
	}
}

// The same two people by first name only: ambiguous, and it must say so rather
// than assign the card to whoever sorted first.
func TestMatchMember_AmbiguousFirstName(t *testing.T) {
	members := []noeto.Member{
		{UserID: "u1", Name: "Michal Bocek", Email: "michal@example.com"},
		{UserID: "u2", Name: "Michaela Novak", Email: "michaela@example.com"},
	}

	if _, err := matchMember("Micha", members); err == nil {
		t.Fatal("expected an ambiguity error")
	}
}

// The subject of a write must be an id. A wrong column is visible; a wrong card
// is a silent edit to work nobody was looking at.
func TestRequireID_RejectsAName(t *testing.T) {
	err := requireID("card", "Fix the login bug")
	if err == nil {
		t.Fatal("expected a name to be rejected")
	}
	// Say where an id comes from, or the model just guesses again.
	if !strings.Contains(err.Error(), "get_board") {
		t.Errorf("error should point at the tool that yields ids: %v", err)
	}
}

func TestRequireID_AcceptsAUUID(t *testing.T) {
	if err := requireID("card", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"); err != nil {
		t.Fatalf("uuid rejected: %v", err)
	}
}

// Spacing and punctuation carry no meaning in a column name, and a model asked
// to move something to To Do will write "Todo" as readily as "To Do". Found by
// driving the real server: the miss cost a turn to an error that then listed
// the very name that had just been typed, minus a space.
func TestMatch_IgnoresSpacingAndPunctuation(t *testing.T) {
	columns := map[string]string{"a": "To Do", "b": "In Progress", "c": "Done"}

	for _, want := range []string{"Todo", "to do", "TODO", "in-progress", "inprogress", "In  Progress"} {
		got, err := match("column", want, columns)
		if err != nil {
			t.Errorf("%q: %v", want, err)
			continue
		}
		if want == "in-progress" || want == "inprogress" || want == "In  Progress" {
			if got != "b" {
				t.Errorf("%q resolved to %q, want In Progress", want, got)
			}
			continue
		}
		if got != "a" {
			t.Errorf("%q resolved to %q, want To Do", want, got)
		}
	}
}

// Normalizing must not collapse names that are genuinely different — "Done"
// and "Not done" still differ by a word, and exact match still wins.
func TestMatch_NormalizingKeepsDistinctNamesDistinct(t *testing.T) {
	got, err := match("column", "done", map[string]string{"a": "Done", "b": "Not done"})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if got != "a" {
		t.Fatalf("got %q, want the exactly-named column", got)
	}
}
