package tools

import (
	"context"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rotisserie/eris"

	"noeto-mcp/internal/noeto"
)

// maxUploadBytes guards this process's memory, since the whole file is held
// for the presigned PUT. The API enforces its own, usually lower, cap and
// names it in its refusal.
const maxUploadBytes = 256 << 20

func (t *server) registerUploads(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "attach_file",
		Description: "Attach a file from this machine to a card, whatever its type — a screenshot, " +
			"a PDF, a log, an archive. Give its absolute path; it goes up under its own name " +
			"unless filename says otherwise. It is added beside what is already on the card, " +
			"never replacing anything. The whole team can open it, so attach only a file the " +
			"user asked for, never one that holds credentials or keys. For a Markdown document " +
			"the card keeps and rewrites, use attach_document instead.",
		Annotations: writes(false),
	}, t.attachFile)
}

type attachFileIn struct {
	Card     string `json:"card" jsonschema:"the card id, from get_board or find_cards"`
	Path     string `json:"path" jsonschema:"absolute path of the file on this machine"`
	Filename string `json:"filename,omitempty" jsonschema:"what to call it on the card; defaults to the file's own name"`
}

func (t *server) attachFile(ctx context.Context, _ *mcp.CallToolRequest, in attachFileIn) (*mcp.CallToolResult, *uploadView, error) {
	if err := requireID("card", in.Card); err != nil {
		return nil, nil, err
	}
	// Absolute only: the agent's working directory is not this process's, so a
	// relative path would quietly resolve somewhere the agent never meant.
	path := strings.TrimSpace(in.Path)
	if !filepath.IsAbs(path) {
		return nil, nil, &badInput{field: "path", want: "an absolute path to a file on this machine", got: in.Path}
	}
	name := in.Filename
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(path)
	}
	filename, err := writableFilename(name)
	if err != nil {
		return nil, nil, err
	}

	body, err := readUploadable(path)
	if err != nil {
		return nil, nil, err
	}

	ready, err := t.api.UploadAttachment(ctx, in.Card, noeto.NewAttachment{
		Filename:    filename,
		ContentType: uploadContentType(filename, body),
		SizeBytes:   int64(len(body)),
	}, body)
	if err != nil {
		return nil, nil, err
	}
	return nil, &uploadView{
		Filename: ready.Filename,
		Type:     baseType(ready.ContentType),
		Bytes:    int64(len(body)),
	}, nil
}

func readUploadable(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, eris.Wrapf(err, "open %s", path)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, eris.Wrapf(err, "inspect %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, eris.Errorf("%s is not a regular file — give the path of one file", path)
	}
	if info.Size() == 0 {
		return nil, eris.Errorf("%s is empty", path)
	}
	if info.Size() > maxUploadBytes {
		return nil, eris.Errorf("%s is %s, past the %s this server will upload", path, size(info.Size()), size(maxUploadBytes))
	}
	body, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil {
		return nil, eris.Wrapf(err, "read %s", path)
	}
	if int64(len(body)) > maxUploadBytes {
		return nil, eris.Errorf("%s grew past the %s this server will upload while it was being read", path, size(maxUploadBytes))
	}
	return body, nil
}

// uploadContentType prefers the extension, which is how every other client
// labels a file, and sniffs only when the extension names nothing. The API
// treats either as a hint and records what the object store reports.
func uploadContentType(filename string, body []byte) string {
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); byExt != "" {
		return byExt
	}
	return http.DetectContentType(body)
}
