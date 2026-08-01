package noeto

import "time"

// The wire shapes of the noeto API, narrowed to the fields this server reads.
//
// Deliberately not generated from openapi.yaml. Nine tools touch nine
// endpoints; a generator would add a build step and several thousand lines of
// client to save the hundred below, and it would still need hand-written
// wrappers to turn "PATCH /cards/{id} with a partial body" into something a
// tool handler can call. What it would buy — catching a contract change — is
// bought instead by the smoke test, which runs against a live API.
//
// Fields the API sends and this server ignores are simply absent here: JSON
// decoding drops them. That is the intended behaviour, not an oversight — a new
// field on the API must not break this client.

type Board struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CardCount   int    `json:"card_count"`
}

type Column struct {
	ID       string `json:"id"`
	BoardID  string `json:"board_id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type Card struct {
	ID          string     `json:"id"`
	BoardID     string     `json:"board_id"`
	ColumnID    string     `json:"column_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	AssigneeID  *string    `json:"assignee_user_id"`
	Priority    *string    `json:"priority"`
	DueDate     *time.Time `json:"due_date"`
	LabelIDs    []string   `json:"label_ids"`
	// Rank is the LexoRank sort key. Read to order a column; never shown to the
	// model and never sent back — moves are expressed as before/after another
	// card and the server assigns the rank.
	Rank            string `json:"rank"`
	CommentCount    int    `json:"comment_count"`
	AttachmentCount int    `json:"attachment_count"`
}

type Label struct {
	ID      string `json:"id"`
	BoardID string `json:"board_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
}

type Comment struct {
	ID         string    `json:"id"`
	AuthorName string    `json:"author_name"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// BoardDetail is one board and everything on it — the response to
// GET /boards/{id}. One request per board is what makes a whole-board snapshot
// affordable, and it is why find_cards can scan a team without N+1 calls.
type BoardDetail struct {
	Board   Board    `json:"board"`
	Columns []Column `json:"columns"`
	Cards   []Card   `json:"cards"`
	Labels  []Label  `json:"labels"`
}

// CardDetail is GET /cards/{id}/detail. Attachments are counted but not
// listed: their URLs are short-lived presigned credentials, and a model has no
// use for a link it cannot open and must not repeat.
type CardDetail struct {
	Card     Card      `json:"card"`
	Comments []Comment `json:"comments"`
}

// CardPatch is the body of PATCH /cards/{id}.
//
// Every field is a pointer because the API distinguishes three states per
// field: absent (leave alone), null (clear), and a value (set). A plain string
// would collapse the first two, which is exactly the bug the API's own handler
// documents having had.
type CardPatch struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	AssigneeID  any       `json:"assignee_user_id,omitempty"`
	Priority    any       `json:"priority,omitempty"`
	DueDate     any       `json:"due_date,omitempty"`
	LabelIDs    *[]string `json:"label_ids,omitempty"`
	Move        *Move     `json:"move,omitempty"`
}

// Move places a card. Position is expressed relative to its neighbours rather
// than as an index or a rank, which is what keeps LexoRank entirely on the
// server — see the note on Card.Rank.
type Move struct {
	ColumnID     string `json:"column_id,omitempty"`
	BeforeCardID string `json:"before_card_id,omitempty"`
	AfterCardID  string `json:"after_card_id,omitempty"`
}

// NewCard is the body of POST /boards/{id}/cards.
type NewCard struct {
	Title       string   `json:"title"`
	ColumnID    string   `json:"column_id"`
	Description *string  `json:"description,omitempty"`
	AssigneeID  *string  `json:"assignee_user_id,omitempty"`
	Priority    *string  `json:"priority,omitempty"`
	DueDate     *string  `json:"due_date,omitempty"`
	LabelIDs    []string `json:"label_ids,omitempty"`
}
