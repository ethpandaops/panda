package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestProxyPassthroughGetHandlesProxyErrorsAndStatuses(t *testing.T) {
	t.Parallel()

	srv := &service{log: logrus.New()}

	rec := httptest.NewRecorder()
	srv.proxyPassthroughGet(rec, httptest.NewRequest(http.MethodGet, "/", nil), "prometheus.query", "/prometheus/api/v1/query", nil, "metrics")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "proxy service is unavailable") {
		t.Fatalf("nil proxy service = (%d, %q), want bad gateway proxy unavailable", rec.Code, rec.Body.String())
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "labels unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	srv = &service{
		log:        logrus.New(),
		httpClient: upstream.Client(),
		proxyService: &stubProxyService{
			url: upstream.URL,
		},
	}

	rec = httptest.NewRecorder()
	srv.proxyPassthroughGet(rec, httptest.NewRequest(http.MethodGet, "/", nil), "prometheus.get_labels", "/prometheus/api/v1/labels", url.Values{"limit": []string{"1"}}, "metrics")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "datasource=metrics") {
		t.Fatalf("upstream status = (%d, %q), want contextual upstream failure", rec.Code, rec.Body.String())
	}
}
