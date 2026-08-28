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
	"regexp"
	"strings"
	"sync"
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

	// me caches the token's own identity. See Me.
	meMu sync.Mutex
	me   *Member
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

// Me is the member this client's access token belongs to.
//
// Cached for the life of the process, and deliberately not invalidated: a token
// cannot change hands, so the answer cannot change either. The lock is held
// across the request so that concurrent first calls make one round trip rather
// than several; a failed call caches nothing, so a transient error does not
// become a permanent one.
//
// The API answers this at /members/me rather than /me because a personal access
// token is admitted only where a team is already the unit of access.
func (c *Client) Me(ctx context.Context) (*Member, error) {
	c.meMu.Lock()
	defer c.meMu.Unlock()

	if c.me != nil {
		return c.me, nil
	}

	var out struct {
		Member Member `json:"member"`
	}
	if err := c.do(ctx, http.MethodGet, "/members/me", nil, &out); err != nil {
		return nil, err
	}
	c.me = &out.Member
	return c.me, nil
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

// ── Attachments ─────────────────────────────────────────────────────────────
//
// An upload is three requests and only the first and last are ours: the API
// reserves a row and hands back a presigned PUT, the bytes go straight to the
// object store, and a completion call reads the size and type back from the
// store rather than trusting what we claimed.
//
// The middle request is the one to be careful with. It goes to a host that is
// not noeto and the presigned URL already carries its own authorization, so the
// access token must not ride along — see putObject.

func (c *Client) ListAttachments(ctx context.Context, cardID string) ([]Attachment, error) {
	var out struct {
		Attachments []Attachment `json:"attachments"`
	}
	path := "/cards/" + url.PathEscape(cardID) + "/attachments"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Attachments, nil
}

// CreateAttachment reserves a pending row and returns where to PUT the bytes.
//
// The row it leaves behind is real: an upload abandoned after this point stays
// pending until somebody deletes it, which is why every caller here has a
// deferred cleanup on the failure path.
func (c *Client) CreateAttachment(ctx context.Context, cardID string, in NewAttachment) (*Upload, error) {
	var out Upload
	path := "/cards/" + url.PathEscape(cardID) + "/attachments"
	if err := c.do(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CompleteAttachment(ctx context.Context, attachmentID string) (*Attachment, error) {
	var out struct {
		Attachment Attachment `json:"attachment"`
	}
	path := "/attachments/" + url.PathEscape(attachmentID) + "/complete"
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out.Attachment, nil
}

// DeleteAttachment removes one. The API lets an uploader delete their own and a
// manager delete anyone's; anything else answers 404, which NotFound reports.
func (c *Client) DeleteAttachment(ctx context.Context, attachmentID string) error {
	return c.do(ctx, http.MethodDelete, "/attachments/"+url.PathEscape(attachmentID), nil, nil)
}

// putObject uploads the bytes to a presigned URL.
//
// Two things this deliberately does not do. It does not send the access token:
// the URL is signed and the host is the object store, not noeto, so attaching a
// noeto credential would hand it to a third party for nothing. And it does not
// add headers of its own — the API signs the exact set it returned, so anything
// extra or missing makes the signature fail with a message about the signature
// rather than about the header.
func (c *Client) putObject(ctx context.Context, uploadURL string, headers map[string]string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		return eris.Wrap(transportCause(err), "build upload request")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	// Explicit, though bytes.Reader already sets it: the presign signs the
	// content length, so a chunked body would be rejected. It is also the only
	// way to set it — Header.Set is a no-op for Content-Length and Host, which
	// Go takes from the request struct. The API sends neither today, so "the
	// exact set it returned" holds; if it ever does, this loop will not carry
	// them and the signature will fail.
	req.ContentLength = int64(len(body))

	res, err := c.http.Do(req)
	if err != nil {
		return &Error{Message: fmt.Sprintf("could not upload to the object store: %v", transportCause(err))}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode >= 400 {
		// The store answers XML, not problem+json, and its text is aimed at
		// somebody debugging a signature. The status is the useful part.
		return &Error{
			Status:  res.StatusCode,
			Message: fmt.Sprintf("the object store rejected the upload with %d", res.StatusCode),
		}
	}
	return nil
}

// transportCause strips the URL out of an HTTP client failure.
//
// Every error from net/http is a *url.Error whose message embeds the request
// URL, and Go redacts only userinfo from it — the query string survives whole.
// For an object-store request that query string IS the credential: the
// signature grants unauthenticated read of the object for as long as the
// presign lives. Left alone, a DNS blip or a timeout would put it in the
// model's context and in whatever the agent host writes to disk, which is
// exactly what returning the content rather than the link exists to prevent.
//
// The wrapped cause — "no such host", "connection refused", "i/o timeout" — is
// the whole of what a caller can act on anyway.
//
// Unwrapping alone is not quite enough. A transport error that quotes a
// response header back — a malformed Location on a bucket redirect, say —
// carries the URL in the inner error's own text, where unwrapping cannot reach
// it. So the message is scrubbed as well as unwrapped: belt and braces on a
// credential, which is the one place that is worth it.
func transportCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if scrubbed := redactQuery(err.Error()); scrubbed != err.Error() {
		// The wrapping is lost, but only for a message that held a credential,
		// and these are terminal messages a caller reads rather than branches on.
		return errors.New(scrubbed)
	}
	return err
}

// signedURL matches an http(s) URL with a query string, wherever it appears in
// a message. The query is the signature; the rest is diagnostics worth keeping.
//
// The `?` excluded from the captured half is load-bearing. Without it the
// greedy quantifier backtracks to the LAST question mark in the run rather than
// the first, so a message holding "…?X-Amz-Signature=…&k=a?b" would keep the
// signature and redact only "b" — the one thing this function exists to stop.
var signedURL = regexp.MustCompile(`(https?://[^\s"'?]*)\?[^\s"']*`)

func redactQuery(message string) string {
	return signedURL.ReplaceAllString(message, "$1?<redacted>")
}

// getObject fetches a presigned download, capped at limit bytes.
//
// Same rule as putObject about the token. The cap is a memory guard rather than
// a policy: the API's own upload limit is what decides how large an attachment
// may be, and this only refuses to hold something absurd in RAM.
func (c *Client) getObject(ctx context.Context, downloadURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, eris.Wrap(transportCause(err), "build download request")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Message: fmt.Sprintf("could not download from the object store: %v", transportCause(err))}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode >= 400 {
		return nil, &Error{
			Status:  res.StatusCode,
			Message: fmt.Sprintf("the object store refused the download with %d", res.StatusCode),
		}
	}

	// limit+1 so a file exactly at the cap is not mistaken for one over it.
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, eris.Wrap(transportCause(err), "read attachment")
	}
	if int64(len(raw)) > limit {
		return nil, &Error{Message: fmt.Sprintf("the attachment is larger than %d bytes", limit)}
	}
	return raw, nil
}

// UploadAttachment runs the whole three-request dance and returns the ready row.
//
// The cleanup is the point of having it here rather than in a tool handler: a
// PUT or a completion that fails leaves a pending row the API will never show
// and never reap on its own, so the failure path deletes what it reserved
// before returning. Best effort — the original error is what the caller needs
// to hear, and a cleanup failure would only bury it.
func (c *Client) UploadAttachment(ctx context.Context, cardID string, in NewAttachment, body []byte) (*Attachment, error) {
	upload, err := c.CreateAttachment(ctx, cardID, in)
	if err != nil {
		return nil, err
	}

	if err := c.putObject(ctx, upload.UploadURL, upload.UploadHeaders, body); err != nil {
		_ = c.DeleteAttachment(context.WithoutCancel(ctx), upload.Attachment.ID)
		return nil, err
	}

	ready, err := c.CompleteAttachment(ctx, upload.Attachment.ID)
	if err != nil {
		_ = c.DeleteAttachment(context.WithoutCancel(ctx), upload.Attachment.ID)
		return nil, err
	}
	return ready, nil
}

// DownloadAttachment fetches an attachment's bytes.
//
// The presigned URL arrives on the listing and expires quickly, so it is used
// here and never returned: a model handed one would repeat it into a transcript
// that outlives it, and it is a bearer credential for as long as it lives.
func (c *Client) DownloadAttachment(ctx context.Context, a Attachment, limit int64) ([]byte, error) {
	if a.DownloadURL == "" {
		return nil, &Error{Message: fmt.Sprintf(
			"%q has no download link — the API could not sign one, which usually means object storage is misconfigured", a.Filename)}
	}
	return c.getObject(ctx, a.DownloadURL, limit)
}
