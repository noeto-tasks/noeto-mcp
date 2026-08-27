package tools

import (
	"bytes"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// The document itself: one HTML file, typeset for a person, with its own
// Markdown source sealed inside it.
//
// HTML rather than Markdown because of how noeto serves attachments. Inline
// preview is an allow-list of image types, so anything textual is downloaded
// rather than opened — and a downloaded .html opens typeset on a double click,
// while a downloaded .md opens as a wall of hashes. The only format that would
// also render inside the app is PDF, which cannot carry its own source.
//
// The web app no longer needs it to: it reads the source block below and renders
// that on the card (document-source.ts), so the trip through Downloads is for
// when somebody wants the file itself. Which is why the delimiters and the
// escaping here are a contract with the frontend, not a private detail.
//
// Sealing the source inside the artifact is what makes the document a carrier
// of context rather than a write-only decoration. read_document gets back exactly
// what was written — no parsing of rendered HTML, no second file to keep in
// step, and nothing to go stale relative to what the reader sees.

// sourceOpen and sourceClose delimit the Markdown source block.
//
// A script element with a type no browser executes is a data block: it is not
// rendered, not run, and survives a round trip through the file untouched. The
// one way to break out of it is the literal text "</script" in the source, and
// escapeSource is what makes that impossible.
const (
	sourceOpen  = `<script type="text/markdown" id="document-source">`
	sourceClose = `</script>`
)

// escapeSource makes Markdown safe to seal inside the script block.
//
// Escaping "<" removes every way to close the element early — "</script" cannot
// survive it — and escaping "&" first is what keeps the transformation
// reversible: without it, a source that already contained "&lt;" would come
// back as "<".
func escapeSource(markdown string) string {
	escaped := strings.ReplaceAll(markdown, "&", "&amp;")
	return strings.ReplaceAll(escaped, "<", "&lt;")
}

// unescapeSource reverses escapeSource. The order mirrors it: "<" first, so an
// "&amp;lt;" becomes "&lt;" rather than "<".
func unescapeSource(escaped string) string {
	unescaped := strings.ReplaceAll(escaped, "&lt;", "<")
	return strings.ReplaceAll(unescaped, "&amp;", "&")
}

// extractSource pulls the Markdown back out of a document.
//
// Deliberately a string search rather than an HTML parse. The escaping
// guarantees the block contains no "<" at all, so the first sourceClose after
// the opening tag is the real end — and a parser would be a dependency and a
// second thing that could disagree with how the file was written.
func extractSource(doc string) (string, error) {
	start := strings.Index(doc, sourceOpen)
	if start < 0 {
		return "", eris.New("this file carries no Markdown source block, so attach_document did not write it — " +
			"read it in a browser instead, and do not overwrite it blind")
	}
	start += len(sourceOpen)

	end := strings.Index(doc[start:], sourceClose)
	if end < 0 {
		return "", eris.New("the Markdown source block is not closed — the file is truncated or corrupt")
	}

	// Exactly the one newline renderDocument writes after the opening tag and
	// before the closing one, so the round trip is byte for byte.
	body := strings.TrimPrefix(doc[start:start+end], "\n")
	body = strings.TrimSuffix(body, "\n")
	return unescapeSource(body), nil
}

// markdown is the renderer, built once.
//
// GFM for the tables, fenced code and strikethrough these documents actually
// use. Raw HTML is left disabled — goldmark's default — which matters more here
// than usual: the file is opened from Downloads as file://, where a script in
// the document would run with local-file privileges, and the Markdown reaching
// this function came from a model reading a card anybody on the team can edit.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// documentSpec is what renderDocument needs to know beyond the Markdown itself.
type documentSpec struct {
	CardTitle string
	CardID    string
	Filename  string
	Markdown  string
	Generated time.Time
}

// renderDocument typesets the document and seals its source into it.
func renderDocument(doc documentSpec) ([]byte, error) {
	var body bytes.Buffer
	if err := markdown.Convert([]byte(doc.Markdown), &body); err != nil {
		return nil, eris.Wrap(err, "render the document")
	}

	title := strings.TrimSpace(doc.CardTitle)
	if title == "" {
		title = doc.Filename
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, documentTemplate,
		html.EscapeString(title),
		documentStyle,
		html.EscapeString(title),
		html.EscapeString(doc.CardID),
		doc.Generated.Format("2006-01-02"),
		body.String(),
		sourceOpen,
		escapeSource(doc.Markdown),
		sourceClose,
	)
	return out.Bytes(), nil
}

// documentTemplate is one template for every document, so two of them a month
// apart look like the same kind of thing.
//
// Self-contained by necessity rather than by taste: it is opened as a local
// file, possibly on a train with no network, so there is no stylesheet to fetch
// and no font to miss. Printable for the same reason — somebody will want a PDF
// of it eventually, and @media print is cheaper than generating one.
const documentTemplate = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
%s</style>
</head>
<body>
<main>
<header class="doc-head">
<h1>%s</h1>
<p class="doc-meta">noeto card %s · %s</p>
</header>
%s</main>
%s
%s
%s
</body>
</html>
`

const documentStyle = `:root {
  --ink: #16181d;
  --muted: #5c6370;
  --rule: #e2e5ea;
  --paper: #ffffff;
  --wash: #f6f7f9;
  --accent: #2f5eff;
}
@media (prefers-color-scheme: dark) {
  :root {
    --ink: #e6e8ec;
    --muted: #9aa1ad;
    --rule: #2c3038;
    --paper: #16181d;
    --wash: #1e2128;
    --accent: #8aa4ff;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--paper);
  color: var(--ink);
  font: 16px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  -webkit-text-size-adjust: 100%;
}
main { max-width: 46rem; margin: 0 auto; padding: 3rem 1.5rem 6rem; }
.doc-head { border-bottom: 2px solid var(--ink); padding-bottom: 1rem; margin-bottom: 2.5rem; }
.doc-head h1 { margin: 0 0 .35rem; font-size: 1.9rem; line-height: 1.2; letter-spacing: -.01em; }
.doc-meta { margin: 0; color: var(--muted); font-size: .82rem; }
main h1, h2 { margin: 2.5rem 0 .75rem; font-size: 1.3rem; line-height: 1.3; }
h3 { margin: 1.75rem 0 .5rem; font-size: 1.05rem; }
h4, h5, h6 { margin: 1.5rem 0 .5rem; font-size: .95rem; }
p, ul, ol, blockquote, table, pre { margin: 0 0 1rem; }
ul, ol { padding-left: 1.4rem; }
li { margin-bottom: .3rem; }
li > ul, li > ol { margin: .3rem 0 .3rem; }
a { color: var(--accent); }
strong { font-weight: 650; }
hr { border: 0; border-top: 1px solid var(--rule); margin: 2.5rem 0; }
blockquote {
  border-left: 3px solid var(--rule);
  margin-left: 0;
  padding: .1rem 0 .1rem 1rem;
  color: var(--muted);
}
code {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  font-size: .87em;
  background: var(--wash);
  border-radius: 3px;
  padding: .12em .35em;
}
pre {
  background: var(--wash);
  border: 1px solid var(--rule);
  border-radius: 6px;
  padding: .85rem 1rem;
  overflow-x: auto;
}
pre code { background: none; padding: 0; font-size: .85rem; }
table { border-collapse: collapse; width: 100%; font-size: .92rem; }
th, td { border: 1px solid var(--rule); padding: .45rem .6rem; text-align: left; vertical-align: top; }
th { background: var(--wash); font-weight: 620; }
img { max-width: 100%; }
@media print {
  :root { --ink: #000; --muted: #444; --rule: #bbb; --paper: #fff; --wash: #f4f4f4; --accent: #000; }
  main { max-width: none; padding: 0; }
  h2, h3 { break-after: avoid; }
  pre, blockquote, table { break-inside: avoid; }
  a { text-decoration: none; }
}
`
