package noeto

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// A presigned URL is a bearer credential: its query string grants
// unauthenticated read of the object for as long as the presign lives. Every
// net/http failure is a *url.Error whose message embeds the whole URL, so
// without this the ordinary failures — a DNS blip, a timeout — would put one in
// the model's context and in whatever the agent host writes to disk.
func TestTransportCause_StripsThePresignedURL(t *testing.T) {
	const signed = "https://store.example.com/attachments/x/y/z.html" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAEXAMPLE&X-Amz-Signature=deadbeefcafe"

	for name, err := range map[string]error{
		// The common shape: the URL is in the *url.Error wrapper, so unwrapping
		// is enough.
		"wrapped": &url.Error{Op: "Get", URL: signed, Err: errors.New("dial tcp: no such host")},

		// The shape unwrapping cannot reach: the store answered with a
		// malformed header and the transport quoted it back, so the URL is in
		// the inner error's own text. A Location header on a bucket redirect is
		// a real way to get here.
		"quoted back": &url.Error{Op: "Get", URL: signed, Err: errors.New(
			`net/http: HTTP/1.x transport connection broken: malformed MIME header line: "Location: ` + signed + `"`)},

		// And with no wrapper at all.
		"bare": errors.New("read tcp: connection reset while fetching " + signed),
	} {
		got := transportCause(err).Error()
		if strings.Contains(got, "X-Amz") || strings.Contains(got, "deadbeefcafe") {
			t.Errorf("%s: the signature survived: %s", name, got)
		}
		if got == "" {
			t.Errorf("%s: the cause was scrubbed away entirely, leaving nothing to act on", name)
		}
	}
}

// Scrubbing must not eat the diagnosis. "no such host" is the whole of what a
// caller can act on.
func TestTransportCause_KeepsTheDiagnosis(t *testing.T) {
	err := &url.Error{
		Op:  "Put",
		URL: "https://store.example.com/o?X-Amz-Signature=abc",
		Err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused"),
	}
	if got := transportCause(err).Error(); got != "dial tcp 10.0.0.1:443: connect: connection refused" {
		t.Errorf("got %q, want the cause intact", got)
	}
}

// An error with no URL in it must come back untouched, wrapping and all.
func TestTransportCause_LeavesAnOrdinaryErrorAlone(t *testing.T) {
	plain := errors.New("unexpected EOF")
	if got := transportCause(plain); !errors.Is(got, plain) {
		t.Errorf("got %v, want the original error unwrapped and unwrapped-from", got)
	}
}

func TestRedactQuery(t *testing.T) {
	for in, want := range map[string]string{
		"https://s.example.com/o?sig=abc":               "https://s.example.com/o?<redacted>",
		"http://s.example.com/o?a=1&b=2 then more text": "http://s.example.com/o?<redacted> then more text",
		`quoted "https://s.example.com/o?sig=abc" here`: `quoted "https://s.example.com/o?<redacted>" here`,
		"https://s.example.com/o":                       "https://s.example.com/o",
		"no url at all":                                 "no url at all",
	} {
		if got := redactQuery(in); got != want {
			t.Errorf("redactQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
