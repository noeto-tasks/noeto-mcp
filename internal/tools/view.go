package tools

import (
	"sort"
	"time"

	"noeto-mcp/internal/noeto"
)

// What a tool returns is not what the API returned.
//
// This is the whole reason the server is hand-written rather than a generic
// OpenAPI bridge. The API's shapes are built for a browser client that keeps
// state: every object carries tenant_id, every card carries a LexoRank sort
// key and a board_id it inherits from its parent, and every relation is a bare
// UUID the client is expected to join against something it already fetched.
//
// A model has no such state. Handed the raw shapes it spends context reading
// identifiers it will never use and cannot resolve — and a board of forty cards
// becomes several thousand tokens of mostly UUID.
//
// So the views below drop what the model cannot act on, resolve what it can
// (assignee and label ids become names), and nest cards inside their column so
// there is no join to perform. Ids survive only where the model needs to send
// one back.

type boardListItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Cards       int    `json:"cards"`
}

type boardView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Columns     []columnView `json:"columns"`
	// Labels the board defines. Listed once here rather than repeated on every
	// card that carries one; cards name them.
	Labels []string `json:"labels,omitempty"`
}

type columnView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Reported only when true, so a board reads as a plain list of lanes until
	// one of them is an endpoint. Initial is where new work lands; final means a
	// card here is no longer work, which is what find_cards leaves out.
	Initial bool       `json:"initial,omitempty"`
	Final   bool       `json:"final,omitempty"`
	Cards   []cardView `json:"cards"`
}

// cardView is a card as a model reads it: the fields that describe the work,
// plus the id needed to act on it.
type cardView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Assignee is who is doing it; CreatedBy is who asked for it. Both, because
	// on a board where the work arrives as requests from other people, "who
	// wants this" is who you go back to when the card and its thread disagree.
	Assignee  string `json:"assignee,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	Priority  string `json:"priority,omitempty"`
	// Due is the calendar day, YYYY-MM-DD. The API sends midnight UTC; the time
	// half is an artifact of the transport and is dropped rather than shown, so
	// a model never has to reason about a timezone that does not exist.
	Due      string   `json:"due,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Comments int      `json:"comments,omitempty"`
	Files    int      `json:"files,omitempty"`
	// Board and Column are set only by find_cards, where results span boards
	// and the nesting that makes them redundant in get_board is gone.
	Board  string `json:"board,omitempty"`
	Column string `json:"column,omitempty"`
}

// cardDetailView adds what the board view leaves out: the full description and
// the comment thread. Named Thread rather than Comments because the embedded
// cardView already has a Comments count, and two fields differing only in type
// is the kind of thing that reads fine and gets used wrong.
type cardDetailView struct {
	cardView
	Description string        `json:"description,omitempty"`
	Thread      []commentView `json:"thread,omitempty"`
	// Files names what cardView only counts, and shadows that count on the way
	// out. On one card the names are the thing — they are what read_attachment
	// is addressed by; get_board and find_cards keep the number, where a list
	// per card would be noise.
	Files []fileView `json:"files,omitempty"`
	// Note fires when the file listing could not be fetched. Losing the whole
	// card because of it would be a poor trade, and an empty list would read as
	// a card with nothing attached, so the failure is named instead.
	Note string `json:"note,omitempty"`
}

// fileView is one attachment as it appears on a card: enough to decide whether
// to read it, and the name read_attachment is asked for. No id, because the
// name addresses it, and no link — the API signs a download URL onto every
// listing and it is a short-lived bearer credential.
type fileView struct {
	Filename   string `json:"filename"`
	Type       string `json:"type,omitempty"`
	Bytes      int64  `json:"bytes"`
	UploadedBy string `json:"uploaded_by,omitempty"`
	When       string `json:"when"`
}

// attachmentView is one attachment's contents. Which of Text and Markdown is
// set says how it was read; for an image neither is, and the pixels travel
// beside this as an image content block.
type attachmentView struct {
	Filename   string `json:"filename"`
	Type       string `json:"type,omitempty"`
	Bytes      int64  `json:"bytes"`
	UploadedBy string `json:"uploaded_by,omitempty"`
	When       string `json:"when"`
	Note       string `json:"note,omitempty"`
	Text       string `json:"text,omitempty"`
	Markdown   string `json:"markdown,omitempty"`
}

type commentView struct {
	Author string `json:"author"`
	When   string `json:"when"`
	Body   string `json:"body"`
}

// documentView and documentSourceView are the two halves of the document pair.
// Neither carries a URL: the API signs a download link on every attachment
// listing, it is a bearer credential with a short life, and a model handed one
// would repeat it into a transcript that outlives it. The content comes back
// instead.
type documentView struct {
	Filename string `json:"filename"`
	Bytes    int    `json:"bytes"`
	// Replaced counts the earlier versions of this document that were deleted
	// after the new one landed.
	Replaced int `json:"replaced,omitempty"`
	// Note names everything the replace deliberately did not touch, so a
	// duplicate left on the card is something somebody knows to clean up.
	Note string `json:"note,omitempty"`
}

// documentSourceView names the uploader on purpose: any team member can put a
// file on a card, so where the source came from is part of reading it.
type documentSourceView struct {
	Filename   string `json:"filename"`
	UploadedBy string `json:"uploaded_by"`
	When       string `json:"when"`
	// Note fires when the card holds more than one file of this name. Only one
	// can be returned, and a silent pick would hide that a choice was made —
	// including the case where somebody else's file is the newer one.
	Note     string `json:"note,omitempty"`
	Markdown string `json:"markdown"`
}

type memberView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// names indexes a board's labels and the team's members, so a card's UUID
// references can be rendered as the words a person would use.
type names struct {
	label  map[string]string
	member map[string]string
}

func newNames(labels []noeto.Label, members []noeto.Member) names {
	n := names{label: map[string]string{}, member: map[string]string{}}
	for _, l := range labels {
		n.label[l.ID] = l.Name
	}
	for _, m := range members {
		n.member[m.UserID] = m.Name
	}
	return n
}

func (n names) card(c noeto.Card) cardView {
	v := cardView{
		ID:       c.ID,
		Title:    c.Title,
		Comments: c.CommentCount,
		Files:    c.AttachmentCount,
	}
	if c.AssigneeID != nil {
		// Fall back to the raw id rather than showing nothing: a member who has
		// left the team is still on the card, and "assigned to someone no longer
		// listed" is information, while a blank field reads as unassigned.
		if name, ok := n.member[*c.AssigneeID]; ok {
			v.Assignee = name
		} else {
			v.Assignee = "(former member)"
		}
	}
	if c.CreatedByID != nil {
		// Same fallback for the same reason. An absent id is different from an
		// unrecognised one: absent means the API never recorded a creator, and
		// that is a blank field rather than a former member.
		if name, ok := n.member[*c.CreatedByID]; ok {
			v.CreatedBy = name
		} else {
			v.CreatedBy = "(former member)"
		}
	}
	if c.Priority != nil {
		v.Priority = *c.Priority
	}
	if c.DueDate != nil {
		v.Due = c.DueDate.Format("2006-01-02")
	}
	for _, id := range c.LabelIDs {
		if name, ok := n.label[id]; ok {
			v.Labels = append(v.Labels, name)
		}
	}
	return v
}

// board nests the cards into their columns, each column in board order and each
// card in rank order — the order a person sees on screen.
func (n names) board(d *noeto.BoardDetail) boardView {
	byColumn := map[string][]noeto.Card{}
	for _, c := range d.Cards {
		byColumn[c.ColumnID] = append(byColumn[c.ColumnID], c)
	}

	columns := append([]noeto.Column(nil), d.Columns...)
	sort.Slice(columns, func(i, j int) bool { return columns[i].Position < columns[j].Position })

	view := boardView{ID: d.Board.ID, Name: d.Board.Name, Description: d.Board.Description}
	for _, l := range d.Labels {
		view.Labels = append(view.Labels, l.Name)
	}

	for _, col := range columns {
		cards := byColumn[col.ID]
		sort.Slice(cards, func(i, j int) bool { return cards[i].Rank < cards[j].Rank })

		cv := columnView{
			ID: col.ID, Name: col.Name,
			Initial: col.IsInitial, Final: col.IsFinal,
			Cards: make([]cardView, 0, len(cards)),
		}
		for _, c := range cards {
			cv.Cards = append(cv.Cards, n.card(c))
		}
		view.Columns = append(view.Columns, cv)
	}
	return view
}

func (n names) comments(list []noeto.Comment) []commentView {
	out := make([]commentView, 0, len(list))
	for _, c := range list {
		out = append(out, commentView{
			Author: c.AuthorName,
			When:   c.CreatedAt.Format(time.RFC3339),
			Body:   c.Body,
		})
	}
	return out
}

func memberViews(list []noeto.Member) []memberView {
	out := make([]memberView, 0, len(list))
	for _, m := range list {
		out = append(out, memberView{ID: m.UserID, Name: m.Name, Email: m.Email, Role: m.Role})
	}
	return out
}

// fileViews renders a card's attachments, newest copy of each name first.
//
// Pending rows are dropped: their object may not exist yet, the API hides them
// from its own listing, and read_attachment would not return one either.
func fileViews(list []noeto.Attachment) []fileView {
	ready := make([]noeto.Attachment, 0, len(list))
	for _, a := range list {
		if a.Status == noeto.AttachmentReady {
			ready = append(ready, a)
		}
	}
	// By name, then newest first within a name, so the duplicates a card can
	// accumulate sit together and the one read_attachment would return leads.
	sort.SliceStable(ready, func(i, j int) bool {
		if ready[i].Filename != ready[j].Filename {
			return ready[i].Filename < ready[j].Filename
		}
		return ready[i].CreatedAt.After(ready[j].CreatedAt)
	})

	out := make([]fileView, 0, len(ready))
	for _, a := range ready {
		out = append(out, fileView{
			Filename:   a.Filename,
			Type:       baseType(a.ContentType),
			Bytes:      a.SizeBytes,
			UploadedBy: a.UploadedBy,
			When:       a.CreatedAt.Format(time.RFC3339),
		})
	}
	return out
}
