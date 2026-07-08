// Package workflowrelay is the shared vocabulary of the two workflow-engine
// relay hops: the panda-server passthrough (pkg/server) and the proxy handler
// (pkg/proxy/handlers). Both forward the same header allow-list, reject the
// same traversal vectors, and speak the same problem+json — keeping those in
// one place means a fix on one hop cannot silently miss the other. Path
// building stays hop-local: the server roots at /workflow, the proxy at
// /api/v1, and their clamp rules genuinely differ.
package workflowrelay

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
)

// ForwardedHeaders is the explicit allow-list of inbound headers a relay hop
// forwards. Everything else (Authorization, Cookie, Host, attribution,
// hop-by-hop) is dropped by construction; each hop injects its own
// Authorization and attribution after filtering.
var ForwardedHeaders = []string{
	"Accept",
	"Content-Type",
	"Idempotency-Key",
	"Last-Event-ID",
}

// FilterHeaders returns a fresh header map holding only the allow-listed
// relay headers from in, with room for the couple of headers the caller
// injects afterwards (Authorization, attribution).
func FilterHeaders(in http.Header) http.Header {
	allowed := make(http.Header, len(ForwardedHeaders)+2)

	for _, name := range ForwardedHeaders {
		if values := in.Values(name); len(values) > 0 {
			key := textproto.CanonicalMIMEHeaderKey(name)
			allowed[key] = append([]string(nil), values...)
		}
	}

	return allowed
}

// RejectTraversal rejects a decoded path that carries any traversal or
// normalization vector: a backslash separator, or a segment that is or contains
// ".." (covers "..", "..;", "...", "..\..") or carries a ";" matrix parameter.
// Workflow path segments never contain "..", ";", or "\", so rejecting outright
// is safe and closes the whole class defensively.
func RejectTraversal(decoded string) error {
	if strings.ContainsRune(decoded, '\\') {
		return errors.New("path traversal is not allowed")
	}

	for seg := range strings.SplitSeq(decoded, "/") {
		if strings.Contains(seg, "..") || strings.Contains(seg, ";") {
			return errors.New("path traversal is not allowed")
		}
	}

	return nil
}

// WriteProblem writes an RFC 7807-style problem+json error body.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w,
		`{"type":"about:blank","title":%q,"status":%d,"detail":%q}`,
		title, status, detail)
}

// IsEventStream reports whether a response Content-Type is a server-sent event
// stream (whose duration must not be recorded as request latency).
func IsEventStream(contentType string) bool {
	return strings.HasPrefix(contentType, "text/event-stream")
}
