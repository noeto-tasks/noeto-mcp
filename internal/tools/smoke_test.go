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

	// created_by is the newest field on the wire and the one with no loud
	// failure mode: it is nullable, so a rename decodes to empty exactly like a
	// card that genuinely predates the feature, and the "asked by" column just
	// quietly goes blank.
	//
	// It therefore cannot be asserted, only counted — and counting is worth
	// more than nothing, because a board where not one card names a creator is
	// either very old or a contract that has moved.
	var withCreator, total int
	for _, col := range board.Columns {
		for _, c := range col.Cards {
			total++
			if c.CreatedBy != "" {
				withCreator++
			}
		}
	}
	switch {
	case total == 0:
	case withCreator == 0:
		t.Logf("none of the %d cards on this board names a creator — either every one of "+
			"them predates the feature, or created_by_user_id stopped decoding. "+
			"Open a recently created card in noeto and check whether it shows an author.", total)
	default:
		t.Logf("created_by decodes: %d of %d cards name a creator", withCreator, total)
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

	// Attachments, read-only. read_document is the one place where a contract
	// change would be invisible until it mattered: the listing is what carries
	// the presigned download URL and the uploader id, and neither has a
	// fallback — no URL means no read, and no uploader id means a replace
	// cannot tell our own document from somebody else's.
	list, err := s.api.ListAttachments(ctx, card.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	for _, a := range list {
		if a.Filename == "" || a.UploadedByID == "" {
			t.Errorf("attachment did not decode: %+v", a)
		}
		if a.Status == noeto.AttachmentReady && a.DownloadURL == "" {
			t.Errorf("a ready attachment came back with no download link: %s", a.Filename)
		}
		// Ordering, not decoration: latestNamed picks the newest copy on this
		// field alone. If it stops decoding, every row is the zero time, After
		// is never true, and "the newest was returned" quietly becomes
		// "whichever the API listed first" — while the note still claims the
		// former, and the next attach_document writes that stale base back.
		if a.CreatedAt.IsZero() {
			t.Errorf("attachment created_at did not decode: %s", a.Filename)
		}
	}
	if len(list) == 0 {
		t.Log("no attachments on this card; the listing shape went unchecked")
	}
}
