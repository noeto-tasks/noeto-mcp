package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noeto-mcp/internal/noeto"
)

// fakeAPI is a stand-in for the noeto API, serving one team with one board.
//
// A fake rather than the real thing, because these tests are about the
// translation layer — resolution, filtering, the three-state patch — and that
// logic is worth exercising in milliseconds without Docker. Whether the shapes
// still match the real API is a different question, answered by the smoke test.
type fakeAPI struct {
	*httptest.Server
	// patched records the raw body of the last PATCH, which is the only way to
	// assert on null-versus-absent: by the time it is a Go value the
	// distinction is gone.
	patched map[string]json.RawMessage
}

const (
	boardID  = "b0000000-0000-4000-8000-000000000001"
	todoID   = "c0000000-0000-4000-8000-000000000001"
	doingID  = "c0000000-0000-4000-8000-000000000002"
	doneID   = "c0000000-0000-4000-8000-000000000003"
	cardID   = "a0000000-0000-4000-8000-000000000001"
	otherID  = "a0000000-0000-4000-8000-000000000002"
	michalID = "u0000000-0000-4000-8000-000000000001"
	annaID   = "u0000000-0000-4000-8000-000000000002"
	bugID    = "1a000000-0000-4000-8000-000000000001"
)

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	fake := &fakeAPI{}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /boards", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"boards": []any{
			map[string]any{"id": boardID, "name": "Roadmap", "card_count": 2},
		}})
	})

	mux.HandleFunc("GET /boards/{id}", serveBoard)
	mux.HandleFunc("GET /members", serveMembers)

	mux.HandleFunc("GET /cards/{id}/detail", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"card": map[string]any{
				"id": cardID, "board_id": boardID, "column_id": todoID,
				"title": "Fix the login bug", "description": "It 500s on Safari.",
				"assignee_user_id": michalID, "label_ids": []string{bugID}, "rank": "b",
			},
			"comments": []any{
				map[string]any{"id": "x", "author_name": "Anna Novak", "body": "Reproduced.", "created_at": "2026-08-01T09:00:00Z"},
			},
		})
	})

	mux.HandleFunc("PATCH /cards/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&fake.patched)
		writeJSON(w, map[string]any{"card": map[string]any{
			"id": cardID, "board_id": boardID, "column_id": doneID,
			"title": "Fix the login bug", "label_ids": []string{}, "rank": "b",
		}})
	})

	mux.HandleFunc("POST /boards/{id}/cards", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&fake.patched)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"card": map[string]any{
			"id": "new", "board_id": boardID, "column_id": doingID,
			"title": "New card", "label_ids": []string{}, "rank": "b",
		}})
	})

	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	return fake
}

// serveBoard and serveMembers are shared with the attachment fixture, which
// needs the same board behind its cards to render one.
func serveBoard(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"board": map[string]any{"id": boardID, "name": "Roadmap"},
		"columns": []any{
			map[string]any{"id": todoID, "board_id": boardID, "name": "Todo", "position": 0},
			map[string]any{"id": doingID, "board_id": boardID, "name": "In Progress", "position": 1},
			map[string]any{"id": doneID, "board_id": boardID, "name": "Done", "position": 2},
		},
		"labels": []any{
			map[string]any{"id": bugID, "board_id": boardID, "name": "bug", "color": "#ff0000"},
		},
		"cards": []any{
			map[string]any{
				"id": cardID, "board_id": boardID, "column_id": todoID,
				"title": "Fix the login bug", "description": "It 500s on Safari.",
				"assignee_user_id": michalID, "created_by_user_id": annaID, "priority": "high",
				"due_date": "2026-08-14T00:00:00Z", "label_ids": []string{bugID},
				"rank": "b", "comment_count": 1,
			},
			map[string]any{
				"id": otherID, "board_id": boardID, "column_id": doneID,
				"title": "Ship the landing page", "label_ids": []string{},
				"created_by_user_id": "u-who-left",
				"rank":               "b",
			},
		},
	})
}

func serveMembers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"members": []any{
		map[string]any{"user_id": michalID, "name": "Michal Bocek", "email": "michal@example.com", "role": "owner"},
		map[string]any{"user_id": annaID, "name": "Anna Novak", "email": "anna@example.com", "role": "member"},
	}})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func newServer(t *testing.T) (*server, *fakeAPI) {
	t.Helper()
	fake := newFakeAPI(t)
	return &server{api: noeto.New(fake.URL, "noeto_pat_test")}, fake
}

// ── find_cards ──────────────────────────────────────────────────────────────

func TestFindCards_ByAssigneeName(t *testing.T) {
	s, _ := newServer(t)

	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Assignee: "Michal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Title != "Fix the login bug" {
		t.Fatalf("got %d cards %v, want just the assigned one", len(cards), cards)
	}
	// Results span boards, so each one has to say where it lives.
	if cards[0].Board != "Roadmap" || cards[0].Column != "Todo" {
		t.Errorf("result is missing its location: board=%q column=%q", cards[0].Board, cards[0].Column)
	}
}

func TestFindCards_Unassigned(t *testing.T) {
	s, _ := newServer(t)

	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Assignee: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Title != "Ship the landing page" {
		t.Fatalf("got %v, want only the unassigned card", cards)
	}
}

func TestFindCards_FiltersCombineWithAnd(t *testing.T) {
	s, _ := newServer(t)

	// Each filter alone matches the login card; together with a column that
	// does not hold it, nothing should come back.
	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Assignee: "Michal", Column: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("got %v, want no results", cards)
	}
}

func TestFindCards_TextSearchesDescriptionsToo(t *testing.T) {
	s, _ := newServer(t)

	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Text: "safari"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want the one whose description mentions Safari", len(cards))
	}
}

func TestFindCards_NoMatchesIsAnEmptyListNotNull(t *testing.T) {
	s, _ := newServer(t)

	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Text: "nothing matches this"})
	if err != nil {
		t.Fatal(err)
	}
	if cards == nil {
		t.Fatal("an empty result must serialize as [], not null")
	}
}

func TestFindCards_RejectsAMalformedDate(t *testing.T) {
	s, _ := newServer(t)

	if _, _, err := s.findCards(context.Background(), nil, findCardsIn{DueBefore: "14/08/2026"}); err == nil {
		t.Fatal("expected a malformed date to be rejected")
	}
}

// ── update_card: the three-state patch ──────────────────────────────────────

// "none" must reach the API as JSON null, which is what it reads as "clear".
// Absent and null are different states there, and this is the only place the
// difference is observable.
func TestUpdateCard_ClearSendsNull(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.updateCard(context.Background(), nil, updateCardIn{Card: cardID, Assignee: "none"})
	if err != nil {
		t.Fatal(err)
	}

	raw, ok := fake.patched["assignee_user_id"]
	if !ok {
		t.Fatal("assignee_user_id was omitted; the API will leave the assignee alone")
	}
	if string(raw) != "null" {
		t.Fatalf("assignee_user_id = %s, want null", raw)
	}
}

// The mirror image: a field nobody mentioned must not appear at all, or an
// unrelated edit silently wipes it.
func TestUpdateCard_OmittedFieldsAreAbsent(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.updateCard(context.Background(), nil, updateCardIn{Card: cardID, Title: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"assignee_user_id", "priority", "due_date", "label_ids"} {
		if _, present := fake.patched[field]; present {
			t.Errorf("%s was sent on a title-only update: %s", field, fake.patched[field])
		}
	}
}

func TestUpdateCard_ResolvesAssigneeByName(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.updateCard(context.Background(), nil, updateCardIn{Card: cardID, Assignee: "Anna"})
	if err != nil {
		t.Fatal(err)
	}

	if got := string(fake.patched["assignee_user_id"]); !strings.Contains(got, annaID) {
		t.Fatalf("assignee_user_id = %s, want Anna's id", got)
	}
}

func TestUpdateCard_RejectsACardGivenByTitle(t *testing.T) {
	s, _ := newServer(t)

	_, _, err := s.updateCard(context.Background(), nil, updateCardIn{Card: "Fix the login bug", Title: "x"})
	if err == nil {
		t.Fatal("a card named rather than identified must be rejected")
	}
}

func TestUpdateCard_RejectsAnUnknownPriority(t *testing.T) {
	s, _ := newServer(t)

	if _, _, err := s.updateCard(context.Background(), nil, updateCardIn{Card: cardID, Priority: "urgent-ish"}); err == nil {
		t.Fatal("expected an unknown priority to be rejected")
	}
}

// ── move_card ───────────────────────────────────────────────────────────────

func TestMoveCard_ResolvesColumnByName(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.moveCard(context.Background(), nil, moveCardIn{Card: cardID, Column: "Done"})
	if err != nil {
		t.Fatal(err)
	}

	var move struct {
		ColumnID string `json:"column_id"`
	}
	if err := json.Unmarshal(fake.patched["move"], &move); err != nil {
		t.Fatalf("no move in the patch: %v", err)
	}
	if move.ColumnID != doneID {
		t.Fatalf("column_id = %q, want the Done column", move.ColumnID)
	}
}

// Position travels as a neighbour, never as a rank — that is what keeps
// LexoRank on the server where it belongs.
func TestMoveCard_SendsNoRank(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.moveCard(context.Background(), nil, moveCardIn{Card: cardID, Column: "Done", AfterCard: otherID})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(fake.patched["move"]), "rank") {
		t.Errorf("a rank leaked into the move: %s", fake.patched["move"])
	}
	if !strings.Contains(string(fake.patched["move"]), otherID) {
		t.Errorf("after_card_id missing: %s", fake.patched["move"])
	}
}

func TestMoveCard_RefusesBothNeighbours(t *testing.T) {
	s, _ := newServer(t)

	_, _, err := s.moveCard(context.Background(), nil, moveCardIn{Card: cardID, BeforeCard: otherID, AfterCard: otherID})
	if err == nil {
		t.Fatal("before_card and after_card together are contradictory and must be rejected")
	}
}

func TestMoveCard_UnknownColumnListsTheRealOnes(t *testing.T) {
	s, _ := newServer(t)

	_, _, err := s.moveCard(context.Background(), nil, moveCardIn{Card: cardID, Column: "Shipped"})
	if err == nil {
		t.Fatal("expected an unknown column to be rejected")
	}
	if !strings.Contains(err.Error(), "Todo") {
		t.Errorf("error should list the board's columns: %v", err)
	}
}

// ── create_card ─────────────────────────────────────────────────────────────

func TestCreateCard_ResolvesEverythingByName(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.createCard(context.Background(), nil, createCardIn{
		Board: "Roadmap", Column: "In Progress", Title: "Write the docs",
		Assignee: "anna@example.com", Priority: "HIGH", Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for field, want := range map[string]string{
		"column_id":        doingID,
		"assignee_user_id": annaID,
		"priority":         "high",
		"label_ids":        bugID,
	} {
		if got := string(fake.patched[field]); !strings.Contains(got, want) {
			t.Errorf("%s = %s, want it to contain %q", field, got, want)
		}
	}
}

func TestCreateCard_UnknownLabelIsRejectedBeforeTheWrite(t *testing.T) {
	s, fake := newServer(t)

	_, _, err := s.createCard(context.Background(), nil, createCardIn{
		Board: "Roadmap", Column: "Todo", Title: "x", Labels: []string{"nonexistent"},
	})
	if err == nil {
		t.Fatal("expected an unknown label to be rejected")
	}
	// Rejected client-side means no half-made card on the board.
	if fake.patched != nil {
		t.Errorf("a card was created despite the bad label: %v", fake.patched)
	}
}

// ── get_card ────────────────────────────────────────────────────────────────

func TestGetCard_IncludesTheThreadAndDescription(t *testing.T) {
	s, _ := newServer(t)

	_, card, err := s.getCard(context.Background(), nil, cardIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if card.Description == "" {
		t.Error("description missing")
	}
	if len(card.Thread) != 1 || card.Thread[0].Author != "Anna Novak" {
		t.Errorf("thread = %v, want one comment by Anna", card.Thread)
	}
	if card.Assignee != "Michal Bocek" {
		t.Errorf("assignee = %q, want the resolved name", card.Assignee)
	}
}

// ── error translation ───────────────────────────────────────────────────────

// A rejected token must produce a message that stops the agent rather than one
// that invites it to retry the same call.
func TestClient_UnauthorizedExplainsItself(t *testing.T) {
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"status": 401, "code": "auth_required", "detail": "Authentication is required."})
	}))
	t.Cleanup(unauthorized.Close)

	s := &server{api: noeto.New(unauthorized.URL, "noeto_pat_stale")}
	_, _, err := s.listBoards(context.Background(), nil, listBoardsIn{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("message should tell the operator what to do: %v", err)
	}
}

// ── who asked for it ────────────────────────────────────────────────────────

// Assignee answers "who is doing this"; the creator answers "who wants it" —
// which is the person to go back to when the card and its thread disagree, and
// the one a board of incoming requests is actually organised around.
func TestFindCards_NamesWhoCreatedTheCard(t *testing.T) {
	s, _ := newServer(t)

	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Text: "safari"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want one", len(cards))
	}
	if cards[0].CreatedBy != "Anna Novak" {
		t.Errorf("created_by = %q, want the resolved name", cards[0].CreatedBy)
	}
	if cards[0].Assignee != "Michal Bocek" {
		t.Errorf("assignee = %q — the two are different people and must not be confused",
			cards[0].Assignee)
	}
}

// A creator who has left the team is still on the card. Saying so beats a blank
// field, which reads as "nobody created this".
func TestFindCards_CreatorWhoLeftTheTeamIsStillNamed(t *testing.T) {
	s, _ := newServer(t)

	_, cards, err := s.findCards(context.Background(), nil, findCardsIn{Text: "landing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want one", len(cards))
	}
	if cards[0].CreatedBy != "(former member)" {
		t.Errorf("created_by = %q, want the former-member marker", cards[0].CreatedBy)
	}
}

// The API records a creator only for cards made since it started recording, and
// the column is ON DELETE SET NULL. Absent is not the same as unrecognised: it
// is a blank field, not a former member.
func TestGetCard_NoCreatorRecordedIsBlank(t *testing.T) {
	s, _ := newServer(t)

	_, card, err := s.getCard(context.Background(), nil, cardIn{Card: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if card.CreatedBy != "" {
		t.Errorf("created_by = %q, want empty when the API sent none", card.CreatedBy)
	}
}
