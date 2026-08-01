package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"noeto-mcp/internal/noeto"
)

func day(s string) *time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &d
}

func str(s string) *string { return &s }

// The board view is what the model actually reads, so its shape is the
// contract: columns in board order, cards in rank order, nested.
func TestBoardView_OrdersColumnsAndCards(t *testing.T) {
	detail := &noeto.BoardDetail{
		Board: noeto.Board{ID: "b1", Name: "Roadmap"},
		// Deliberately out of order on the wire.
		Columns: []noeto.Column{
			{ID: "c2", Name: "Done", Position: 1},
			{ID: "c1", Name: "Todo", Position: 0},
		},
		Cards: []noeto.Card{
			{ID: "k2", ColumnID: "c1", Title: "second", Rank: "n"},
			{ID: "k1", ColumnID: "c1", Title: "first", Rank: "b"},
			{ID: "k3", ColumnID: "c2", Title: "shipped", Rank: "b"},
		},
	}

	view := newNames(nil, nil).board(detail)

	if len(view.Columns) != 2 {
		t.Fatalf("got %d columns, want 2", len(view.Columns))
	}
	if view.Columns[0].Name != "Todo" || view.Columns[1].Name != "Done" {
		t.Errorf("columns out of board order: %s, %s", view.Columns[0].Name, view.Columns[1].Name)
	}
	if got := view.Columns[0].Cards; got[0].Title != "first" || got[1].Title != "second" {
		t.Errorf("cards out of rank order: %s, %s", got[0].Title, got[1].Title)
	}
}

// A column with no cards must still appear — "Done is empty" is an answer, and
// omitting the column makes it look like the board has no such stage.
func TestBoardView_KeepsEmptyColumns(t *testing.T) {
	detail := &noeto.BoardDetail{
		Board:   noeto.Board{ID: "b1", Name: "Roadmap"},
		Columns: []noeto.Column{{ID: "c1", Name: "Todo", Position: 0}, {ID: "c2", Name: "Done", Position: 1}},
	}

	view := newNames(nil, nil).board(detail)
	if len(view.Columns) != 2 {
		t.Fatalf("got %d columns, want both", len(view.Columns))
	}
	if view.Columns[1].Cards == nil {
		t.Error("an empty column should serialize as [], not null")
	}
}

// Assignees and labels arrive as UUIDs and must leave as words — this is the
// single biggest thing the view layer buys.
func TestCardView_ResolvesNames(t *testing.T) {
	render := newNames(
		[]noeto.Label{{ID: "l1", Name: "bug"}, {ID: "l2", Name: "urgent"}},
		[]noeto.Member{{UserID: "u1", Name: "Michal"}},
	)

	view := render.card(noeto.Card{
		ID: "k1", Title: "Fix login", AssigneeID: str("u1"), LabelIDs: []string{"l1", "l2"},
	})

	if view.Assignee != "Michal" {
		t.Errorf("assignee = %q, want the name", view.Assignee)
	}
	if strings.Join(view.Labels, ",") != "bug,urgent" {
		t.Errorf("labels = %v, want names", view.Labels)
	}
}

// Someone who left the team is still on the card. Blank would read as
// unassigned, which is a different fact.
func TestCardView_MarksAFormerMember(t *testing.T) {
	view := newNames(nil, nil).card(noeto.Card{ID: "k1", AssigneeID: str("gone")})

	if view.Assignee == "" {
		t.Fatal("an unresolvable assignee must not render as unassigned")
	}
	if strings.Contains(view.Assignee, "gone") {
		t.Errorf("a raw uuid leaked into the view: %q", view.Assignee)
	}
}

// The API sends midnight UTC; the time half is a transport artifact and must
// not reach the model, which would otherwise reason about a timezone that is
// not part of the data.
func TestCardView_DueDateIsACalendarDay(t *testing.T) {
	view := newNames(nil, nil).card(noeto.Card{ID: "k1", DueDate: day("2026-08-14")})

	if view.Due != "2026-08-14" {
		t.Errorf("due = %q, want a plain calendar day", view.Due)
	}
}

// Nothing the model cannot act on should reach it — this is the token budget
// the whole view layer exists to protect.
func TestCardView_DropsInternalFields(t *testing.T) {
	encoded, err := json.Marshal(newNames(nil, nil).card(noeto.Card{
		ID: "k1", BoardID: "b1", ColumnID: "c1", Title: "x", Rank: "aaaa",
	}))
	if err != nil {
		t.Fatal(err)
	}

	for _, leaked := range []string{"rank", "aaaa", "tenant"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("%q reached the model: %s", leaked, encoded)
		}
	}
}
