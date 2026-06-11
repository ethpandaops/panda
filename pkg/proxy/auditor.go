package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/attribution"
	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

// defaultMaxAuditBodyBytes caps how much of a captured request/response body is
// stored per audit entry when AuditConfig.MaxBodyBytes is unset.
const defaultMaxAuditBodyBytes = 64 * 1024

// Auditor logs audit entries for proxy requests.
type Auditor struct {
	log             logrus.FieldLogger
	logRequestBody  bool
	logResponseBody bool
	maxBodyBytes    int
}

// NewAuditor creates a new auditor.
func NewAuditor(log logrus.FieldLogger, cfg AuditConfig) *Auditor {
	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxAuditBodyBytes
	}

	// Request body logging defaults to on when unset.
	logRequestBody := cfg.LogRequestBody == nil || *cfg.LogRequestBody

	return &Auditor{
		log:             log.WithField("component", "auditor"),
		logRequestBody:  logRequestBody,
		logResponseBody: cfg.LogResponseBody,
		maxBodyBytes:    maxBodyBytes,
	}
}

// Middleware returns an HTTP middleware that logs audit entries.
func (a *Auditor) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Tee the request body so we can audit it while still forwarding the
			// full payload upstream. The capped buffer fills as the downstream
			// handler reads the body.
			var reqBody *cappedBuffer
			if a.logRequestBody && r.Body != nil {
				reqBody = &cappedBuffer{limit: a.maxBodyBytes}
				r.Body = readCloser{Reader: io.TeeReader(r.Body, reqBody), Closer: r.Body}
			}

			// Wrap response writer to capture status code and bytes, plus an
			// optional capped copy of the response body.
			wrapped := &responseCapture{ResponseWriter: w, statusCode: http.StatusOK}
			if a.logResponseBody {
				wrapped.bodyCapture = &cappedBuffer{limit: a.maxBodyBytes}
			}

			// Call next handler.
			next.ServeHTTP(wrapped, r)

			// Build audit fields.
			fields := logrus.Fields{
				"method":          r.Method,
				"path":            r.URL.Path,
				"remote_addr":     r.RemoteAddr,
				"datasource_type": extractDatasourceType(r.URL.Path),
				"status":          wrapped.statusCode,
				"request_bytes":   r.ContentLength,
				"response_bytes":  wrapped.bytesWritten,
				"duration_ms":     time.Since(start).Milliseconds(),
			}

			// Resolve user identity from auth context.
			if proxyUser := GetAuthUser(r.Context()); proxyUser != nil {
				fields["subject"] = proxyUser.Subject
				fields["username"] = proxyUser.Username

				if len(proxyUser.Groups) > 0 {
					fields["groups"] = proxyUser.Groups
				}
			} else if authUser := simpleauth.GetAuthUser(r.Context()); authUser != nil {
				fields["subject"] = authUser.Subject
				fields["username"] = authUser.Username

				if len(authUser.Groups) > 0 {
					fields["groups"] = authUser.Groups
				}
			}

			if ds := r.Header.Get(handlers.DatasourceHeader); ds != "" {
				fields["datasource_name"] = ds
			}

			// Caller-supplied attribution (e.g. the human a chat agent acts
			// for). Untrusted free-text: audit context only.
			if v := r.Header.Get(attribution.Header); v != "" {
				fields["on_behalf_of"] = v
			}

			if r.URL.RawQuery != "" {
				fields["query_string"] = r.URL.RawQuery
			}

			if ua := r.UserAgent(); ua != "" {
				fields["user_agent"] = ua
			}

			if reqBody != nil && reqBody.len > 0 {
				fields["request_body"] = reqBody.String()
				fields["request_body_truncated"] = reqBody.truncated
			}

			if wrapped.bodyCapture != nil && wrapped.bodyCapture.len > 0 {
				fields["response_body"] = wrapped.bodyCapture.String()
				fields["response_body_truncated"] = wrapped.bodyCapture.truncated
			}

			a.log.WithFields(fields).Info("Audit")
		})
	}
}

// cappedBuffer accumulates written bytes up to limit and records whether more
// data was discarded. It always reports a full write so it is safe as an
// io.TeeReader sink and never short-circuits the underlying copy.
type cappedBuffer struct {
	buf       []byte
	limit     int
	len       int
	truncated bool
}

// Write appends up to the remaining capacity and tracks truncation.
func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.len += len(p)

	if rem := c.limit - len(c.buf); rem > 0 {
		if len(p) > rem {
			c.buf = append(c.buf, p[:rem]...)
			c.truncated = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}

	return len(p), nil
}

// String returns the captured prefix of the body.
func (c *cappedBuffer) String() string {
	return string(c.buf)
}

// readCloser pairs a (tee'd) reader with the original body's Closer so the
// upstream handler can still close the request body.
type readCloser struct {
	io.Reader
	io.Closer
}
