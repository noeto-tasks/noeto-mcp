package tools

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"noeto-mcp/internal/noeto"
)

func (t *server) registerBoards(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_boards",
		Description: "List the boards in the team, with a card count for each. " +
			"Start here when you do not know which board the work is on.",
	}, t.listBoards)

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_board",
		Description: "Read a whole board: its columns in order, and the cards in each " +
			"column in the order they appear on screen. Assignees and labels are given " +
			"by name. This is the call that gives you the card ids everything else needs.",
	}, t.getBoard)

	mcp.AddTool(s, &mcp.Tool{
		Name: "find_cards",
		Description: "Search cards across every board in the team. All filters are " +
			"optional and combine with AND. Use this for questions like \"what is " +
			"assigned to me\" or \"what is overdue\"; use get_board when you already " +
			"know the board and want to see its shape.",
	}, t.findCards)
}

type listBoardsIn struct{}

func (t *server) listBoards(ctx context.Context, _ *mcp.CallToolRequest, _ listBoardsIn) (*mcp.CallToolResult, []boardListItem, error) {
	boards, err := t.api.ListBoards(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := make([]boardListItem, 0, len(boards))
	for _, b := range boards {
		out = append(out, boardListItem{
			ID: b.ID, Name: b.Name, Description: b.Description, Cards: b.CardCount,
		})
	}
	return nil, out, nil
}

type getBoardIn struct {
	Board string `json:"board" jsonschema:"the board, by name or id"`
}

func (t *server) getBoard(ctx context.Context, _ *mcp.CallToolRequest, in getBoardIn) (*mcp.CallToolResult, *boardView, error) {
	detail, members, err := t.board(ctx, in.Board)
	if err != nil {
		return nil, nil, err
	}
	view := newNames(detail.Labels, members).board(detail)
	return nil, &view, nil
}

type findCardsIn struct {
	Text      string `json:"text,omitempty" jsonschema:"match against card titles and descriptions, case-insensitive"`
	Assignee  string `json:"assignee,omitempty" jsonschema:"a team member by name email or id; the literal 'none' finds unassigned cards"`
	Label     string `json:"label,omitempty" jsonschema:"a label name"`
	Priority  string `json:"priority,omitempty" jsonschema:"low, medium or high"`
	Column    string `json:"column,omitempty" jsonschema:"a column name, matched across boards"`
	Board     string `json:"board,omitempty" jsonschema:"restrict to one board, by name or id"`
	DueBefore string `json:"due_before,omitempty" jsonschema:"YYYY-MM-DD; cards due on or before this day"`
	DueAfter  string `json:"due_after,omitempty" jsonschema:"YYYY-MM-DD; cards due on or after this day"`
	Overdue   bool   `json:"overdue,omitempty" jsonschema:"only cards whose due date has passed"`
}

// findCards filters in this process, because the API has no search.
//
// It fetches every board in the team and scans the result. That is the honest
// trade for a team-sized dataset: a board snapshot is one request, boards are
// fetched concurrently, and a team with a dozen boards and a few hundred cards
// answers in well under a second. Adding a search endpoint to the API would be
// the right fix at ten times that, and the wrong one before — it is a schema, a
// migration and an index in exchange for latency nobody can perceive.
//
// What this must not become is a paginated crawl. If a team grows past what one
// fan-out can hold, the answer is a server-side query, not a smarter client.
func (t *server) findCards(ctx context.Context, _ *mcp.CallToolRequest, in findCardsIn) (*mcp.CallToolResult, []cardView, error) {
	members, err := t.api.ListMembers(ctx)
	if err != nil {
		return nil, nil, err
	}

	var wantAssignee string
	unassignedOnly := strings.EqualFold(strings.TrimSpace(in.Assignee), "none")
	if in.Assignee != "" && !unassignedOnly {
		wantAssignee, err = matchMember(in.Assignee, members)
		if err != nil {
			return nil, nil, err
		}
	}

	dueBefore, err := parseDay(in.DueBefore, "due_before")
	if err != nil {
		return nil, nil, err
	}
	dueAfter, err := parseDay(in.DueAfter, "due_after")
	if err != nil {
		return nil, nil, err
	}

	boards, err := t.scope(ctx, in.Board)
	if err != nil {
		return nil, nil, err
	}

	details, err := t.fetchBoards(ctx, boards)
	if err != nil {
		return nil, nil, err
	}

	text := strings.ToLower(strings.TrimSpace(in.Text))
	label := strings.ToLower(strings.TrimSpace(in.Label))
	column := strings.ToLower(strings.TrimSpace(in.Column))
	priority := strings.ToLower(strings.TrimSpace(in.Priority))
	today := time.Now().UTC().Truncate(24 * time.Hour)

	var out []cardView
	for _, d := range details {
		render := newNames(d.Labels, members)
		columns := columnIndex(d.Columns)
		labels := labelIndex(d.Labels)

		for _, c := range d.Cards {
			switch {
			case text != "" && !strings.Contains(strings.ToLower(c.Title), text) &&
				!strings.Contains(strings.ToLower(c.Description), text):
				continue
			case unassignedOnly && c.AssigneeID != nil:
				continue
			case wantAssignee != "" && (c.AssigneeID == nil || *c.AssigneeID != wantAssignee):
				continue
			case priority != "" && (c.Priority == nil || !strings.EqualFold(*c.Priority, priority)):
				continue
			case column != "" && !strings.Contains(strings.ToLower(columns[c.ColumnID]), column):
				continue
			case label != "" && !hasLabel(c.LabelIDs, labels, label):
				continue
			case in.Overdue && (c.DueDate == nil || !c.DueDate.Before(today)):
				continue
			case dueBefore != nil && (c.DueDate == nil || c.DueDate.After(*dueBefore)):
				continue
			case dueAfter != nil && (c.DueDate == nil || c.DueDate.Before(*dueAfter)):
				continue
			}

			view := render.card(c)
			// Results span boards, so each card says where it lives — the
			// nesting that makes this redundant in get_board is gone here.
			view.Board = d.Board.Name
			view.Column = columns[c.ColumnID]
			out = append(out, view)
		}
	}

	// Soonest due first, undated last: an agent asked "what should I do" reads
	// the top of the list, so the ordering is part of the answer.
	sort.SliceStable(out, func(i, j int) bool {
		switch {
		case out[i].Due == out[j].Due:
			return out[i].Title < out[j].Title
		case out[i].Due == "":
			return false
		case out[j].Due == "":
			return true
		default:
			return out[i].Due < out[j].Due
		}
	})

	if out == nil {
		out = []cardView{}
	}
	return nil, out, nil
}

// scope is the set of boards to search: one named board, or all of them.
func (t *server) scope(ctx context.Context, nameOrID string) ([]noeto.Board, error) {
	boards, err := t.api.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(nameOrID) == "" {
		return boards, nil
	}

	id, err := match("board", nameOrID, boardIndex(boards))
	if err != nil {
		return nil, err
	}
	for _, b := range boards {
		if b.ID == id {
			return []noeto.Board{b}, nil
		}
	}
	return nil, nil
}

// fetchBoards loads every board's snapshot concurrently.
//
// Bounded, because a team with fifty boards would otherwise open fifty
// simultaneous connections against a single-instance API and turn a search into
// a small denial of service against its owner.
func (t *server) fetchBoards(ctx context.Context, boards []noeto.Board) ([]*noeto.BoardDetail, error) {
	const parallel = 6

	details := make([]*noeto.BoardDetail, len(boards))
	errs := make([]error, len(boards))

	var wg sync.WaitGroup
	slots := make(chan struct{}, parallel)

	for i, b := range boards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			details[i], errs[i] = t.api.GetBoard(ctx, b.ID)
		}()
	}
	wg.Wait()

	out := make([]*noeto.BoardDetail, 0, len(details))
	for i, d := range details {
		// A board that vanished between the list and the fetch is not a failed
		// search — skip it. Any other error is real and stops the call, because
		// silently searching four boards out of five and reporting "no results"
		// is worse than an error.
		if errs[i] != nil {
			if noeto.NotFound(errs[i]) {
				continue
			}
			return nil, errs[i]
		}
		out = append(out, d)
	}
	return out, nil
}

func hasLabel(ids []string, labels map[string]string, want string) bool {
	for _, id := range ids {
		if strings.Contains(strings.ToLower(labels[id]), want) {
			return true
		}
	}
	return false
}

func parseDay(value, field string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	day, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, &badInput{field: field, want: "a date as YYYY-MM-DD", got: value}
	}
	return &day, nil
}
