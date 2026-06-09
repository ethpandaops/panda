package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditorLogsRequestBodyWhileForwardingFullPayload(t *testing.T) {
	const sql = "SELECT count() FROM beacon_api_eth_v1_events_block WHERE meta_network_name = 'mainnet'"

	tests := []struct {
		name            string
		cfg             AuditConfig
		body            string
		wantField       bool
		wantTruncated   bool
		wantStoredEqual string
	}{
		{
			name:            "captures full request body",
			cfg:             AuditConfig{Enabled: true, LogRequestBody: boolPtr(true)},
			body:            sql,
			wantField:       true,
			wantStoredEqual: sql,
		},
		{
			name:            "enabled by default when unset",
			cfg:             AuditConfig{Enabled: true},
			body:            sql,
			wantField:       true,
			wantStoredEqual: sql,
		},
		{
			name:          "truncates to max_body_bytes",
			cfg:           AuditConfig{Enabled: true, LogRequestBody: boolPtr(true), MaxBodyBytes: 10},
			body:          sql,
			wantField:     true,
			wantTruncated: true,
		},
		{
			name:      "explicitly disabled",
			cfg:       AuditConfig{Enabled: true, LogRequestBody: boolPtr(false)},
			body:      sql,
			wantField: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, hook := test.NewNullLogger()
			log.SetLevel(logrus.InfoLevel)
			auditor := NewAuditor(log, tt.cfg)

			// Downstream handler must observe the complete, untruncated body.
			var upstream string
			handler := auditor.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				upstream = string(b)
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tt.body, upstream, "upstream must receive the full body")

			entry := lastAuditEntry(t, hook)
			stored, ok := entry.Data["request_body"]
			assert.Equal(t, tt.wantField, ok, "request_body presence")

			if tt.wantField {
				if tt.wantStoredEqual != "" {
					assert.Equal(t, tt.wantStoredEqual, stored)
				}
				assert.Equal(t, tt.wantTruncated, entry.Data["request_body_truncated"])
			}
		})
	}
}

func TestAuditorCapturesResponseBody(t *testing.T) {
	log, hook := test.NewNullLogger()
	log.SetLevel(logrus.InfoLevel)
	auditor := NewAuditor(log, AuditConfig{Enabled: true, LogResponseBody: true})

	const payload = "row1\trow2\trow3"
	rec := httptest.NewRecorder()
	handler := auditor.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil)))

	assert.Equal(t, payload, rec.Body.String(), "client must receive the full response")

	entry := lastAuditEntry(t, hook)
	assert.Equal(t, payload, entry.Data["response_body"])
	assert.Equal(t, false, entry.Data["response_body_truncated"])
}

func boolPtr(b bool) *bool {
	return &b
}

func lastAuditEntry(t *testing.T, hook *test.Hook) *logrus.Entry {
	t.Helper()

	for i := len(hook.Entries) - 1; i >= 0; i-- {
		if hook.Entries[i].Message == "Audit" {
			return &hook.Entries[i]
		}
	}

	t.Fatal("no audit entry emitted")

	return nil
}
