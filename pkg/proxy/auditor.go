package proxy

import (
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

// Auditor logs audit entries for proxy requests.
type Auditor struct {
	log logrus.FieldLogger
}

// NewAuditor creates a new auditor.
func NewAuditor(log logrus.FieldLogger) *Auditor {
	return &Auditor{
		log: log.WithField("component", "auditor"),
	}
}

// Middleware returns an HTTP middleware that logs audit entries.
func (a *Auditor) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code and bytes.
			wrapped := &responseCapture{ResponseWriter: w, statusCode: http.StatusOK}

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

			if r.URL.RawQuery != "" {
				fields["query_string"] = r.URL.RawQuery
			}

			if ua := r.UserAgent(); ua != "" {
				fields["user_agent"] = ua
			}

			a.log.WithFields(fields).Info("Audit")
		})
	}
}
