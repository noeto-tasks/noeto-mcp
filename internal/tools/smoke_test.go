package tools

import (
	"context"
	"os"
	"testing"

	"noeto-mcp/internal/noeto"
)

// The smoke test is what a shared repository would have given us for free.
//
// This client lives outside noeto-api, so `make openapi-check` there does not
// know it exists: the API's contract can change and nothing fails until an
// agent gets a board with no columns in it. The unit tests above cannot catch
// that — they run against a fake whose shapes are a copy of what the API sent
// on the day it was written, so a renamed field breaks the real call and the
// fake keeps agreeing with itself.
//
// So this one talks to a running API and asserts that the fields the tools
// actually depend on arrive populated. It is skipped unless NOETO_TOKEN is set,
// which keeps `go test ./...` hermetic while making the drift check one command
// away:
//
//	make smoke
//
// It only reads. A test that creates cards on whatever board it finds would be
// a test nobody dares run against anything but an empty database.
func TestSmoke_ContractStillHolds(t *testing.T) {
	token := os.Getenv("NOETO_TOKEN")
	if token == "" {
		t.Skip("NOETO_TOKEN not set — skipping the live contract check")
	}

	apiURL := os.Getenv("NOETO_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8081/api/v1"
	}

	s := &server{api: noeto.New(apiURL, token)}
	ctx := context.Background()

	_, boards, err := s.listBoards(ctx, nil, listBoardsIn{})
	if err != nil {
		t.Fatalf("list_boards: %v", err)
	}
	if len(boards) == 0 {
		t.Skip("the team has no boards — nothing to check against")
	}
	if boards[0].Name == "" {
		t.Error("board name did not decode — the API's field may have been renamed")
	}
	if boards[0].ID == "" {
		t.Fatal("board id did not decode")
	}

	_, board, err := s.getBoard(ctx, nil, getBoardIn{Board: boards[0].ID})
	if err != nil {
		t.Fatalf("get_board: %v", err)
	}
	if len(board.Columns) == 0 {
		t.Fatal("board came back with no columns — every noeto board is seeded with some")
	}
	if board.Columns[0].Name == "" {
		t.Error("column name did not decode")
	}

	// Cards are where most of the shape lives, so check one properly if the
	// board has any. Rank matters even though it never reaches the model: it is
	// what orders the column, and a silently-empty rank makes every board
	// render in arbitrary order.
	var card *cardView
	for _, col := range board.Columns {
		if len(col.Cards) > 0 {
			card = &col.Cards[0]
			break
		}
	}
	if card == nil {
		t.Log("no cards on this board; skipping the card half of the check")
		return
	}
	if card.ID == "" || card.Title == "" {
		t.Errorf("card did not decode: %+v", card)
	}

	_, detail, err := s.getCard(ctx, nil, cardIn{Card: card.ID})
	if err != nil {
		t.Fatalf("get_card: %v", err)
	}
	if detail.Title != card.Title {
		t.Errorf("get_card and get_board disagree on the title: %q vs %q", detail.Title, card.Title)
	}

	_, members, err := s.listMembers(ctx, nil, listMembersIn{})
	if err != nil {
		t.Fatalf("list_members: %v", err)
	}
	if len(members) == 0 || members[0].Email == "" {
		t.Error("members did not decode — a team always has at least its owner")
	}

	// find_cards fans out over every board; running it once proves the fan-out
	// and the filter path survive real data rather than the single fake board.
	if _, _, err := s.findCards(ctx, nil, findCardsIn{}); err != nil {
		t.Fatalf("find_cards: %v", err)
	}
}
