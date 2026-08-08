// Package noeto is an HTTP client for the noeto API, holding a personal access
// token.
//
// It is the only thing in this server that knows about HTTP. Tool handlers call
// typed methods and get typed values or an Error; they never see a status code,
// and they never build a URL.
package noeto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rotisserie/eris"
)

// Client talks to one noeto deployment as one user in one team.
//
// One team, because a personal access token is bound to the team it was issued
// in. That is a property of the credential, not a limitation of this client:
// serving two teams means two tokens and two server processes. It is also why
// no method here takes a team id — there is only ever one, and the API would
// refuse to be told otherwise.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client. baseURL is the API root including /api/v1.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// Generous but finite: an agent waiting on a hung request is worse than
		// one told the request failed, because it cannot tell the difference
		// between slow and stuck.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Error is a failed API call, carrying the stable machine-readable code the API
// documents. Tool handlers branch on Code; the model reads Message.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// problem is the RFC 9457 body the API answers every failure with.
type problem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// do performs a request and decodes into out, which may be nil for 204s.
//
// The error path is the interesting half. Whatever comes back becomes an
// *Error with a message written for a model rather than for a log: a model that
// reads "401" retries the same call, while one that reads "the access token was
// rejected" stops and says so.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return eris.Wrap(err, "encode request")
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return eris.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return &Error{Message: fmt.Sprintf("could not reach the noeto API at %s: %v%s",
			c.baseURL, err, containerLoopbackHint(c.baseURL))}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode >= 400 {
		return c.apiError(res)
	}

	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return eris.Wrapf(err, "decode %s %s", method, path)
	}
	return nil
}

// apiError turns a failure response into an *Error.
//
// Defensive about the body on purpose: a 502 from a CDN is an HTML page, not
// problem+json, and the status is more useful than a decode error raised inside
// the error path.
func (c *Client) apiError(res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))

	var p problem
	_ = json.Unmarshal(raw, &p)

	message := p.Detail
	if message == "" {
		message = fmt.Sprintf("the noeto API answered %d", res.StatusCode)
	}

	// Three codes get rewritten, because the API's own wording is aimed at a
	// person reading docs and leaves a model with no next action.
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		message = "the noeto access token was rejected — it may have been revoked or expired. " +
			"A new one is issued in noeto under Settings → Access tokens."
	case p.Code == "session_required":
		// Should be unreachable: every operation this server calls is
		// team-scoped. If it fires, a tool is calling something it must not.
		message = "that operation cannot be performed with an access token; it needs a signed-in browser session."
	case p.Code == "team_not_selected":
		message = "the access token is not bound to a team, which should be impossible — reissue it."
	}

	return &Error{Status: res.StatusCode, Code: p.Code, Message: message}
}

// NotFound reports whether err is the API saying the resource does not exist.
// A wrong id and another team's id answer alike, by design.
func NotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// ── Reads ───────────────────────────────────────────────────────────────────

func (c *Client) ListBoards(ctx context.Context) ([]Board, error) {
	var out struct {
		Boards []Board `json:"boards"`
	}
	if err := c.do(ctx, http.MethodGet, "/boards", nil, &out); err != nil {
		return nil, err
	}
	return out.Boards, nil
}

func (c *Client) GetBoard(ctx context.Context, boardID string) (*BoardDetail, error) {
	var out BoardDetail
	if err := c.do(ctx, http.MethodGet, "/boards/"+url.PathEscape(boardID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetCard(ctx context.Context, cardID string) (*CardDetail, error) {
	var out CardDetail
	if err := c.do(ctx, http.MethodGet, "/cards/"+url.PathEscape(cardID)+"/detail", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListMembers(ctx context.Context) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	if err := c.do(ctx, http.MethodGet, "/members", nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// ── Writes ──────────────────────────────────────────────────────────────────

func (c *Client) CreateCard(ctx context.Context, boardID string, in NewCard) (*Card, error) {
	var out struct {
		Card Card `json:"card"`
	}
	path := "/boards/" + url.PathEscape(boardID) + "/cards"
	if err := c.do(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	return &out.Card, nil
}

func (c *Client) PatchCard(ctx context.Context, cardID string, patch CardPatch) (*Card, error) {
	var out struct {
		Card Card `json:"card"`
	}
	if err := c.do(ctx, http.MethodPatch, "/cards/"+url.PathEscape(cardID), patch, &out); err != nil {
		return nil, err
	}
	return &out.Card, nil
}

func (c *Client) AddComment(ctx context.Context, cardID, body string) (*Comment, error) {
	var out struct {
		Comment Comment `json:"comment"`
	}
	in := struct {
		Body string `json:"body"`
	}{Body: body}
	path := "/cards/" + url.PathEscape(cardID) + "/comments"
	if err := c.do(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	return &out.Comment, nil
}

// containerLoopbackHint explains the one failure everybody hits first when
// running this image against a local noeto.
//
// Inside a container, localhost is the container — so NOETO_API_URL pointing at
// 127.0.0.1 reaches nothing, and the error is a bare connection refused that
// says nothing about why. The fix differs by platform, so the hint names both.
//
// Not a startup check, because the combination is not always wrong: `docker run
// --network host` on Linux makes loopback work exactly as written. It is only
// ever a hint, attached to a failure that already happened.
func containerLoopbackHint(baseURL string) string {
	if !inContainer() {
		return ""
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
	default:
		return ""
	}

	return "\n\nThis is running in a container, where localhost is the container itself. " +
		"To reach a noeto on the host, set NOETO_API_URL to " +
		"http://host.docker.internal:8081/api/v1 (macOS and Windows), " +
		"or run the container with --network host (Linux)."
}

// inContainer reports whether this process is running inside a Docker
// container. /.dockerenv is created by the daemon and is the cheapest reliable
// signal; a false negative only costs the hint.
func inContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
