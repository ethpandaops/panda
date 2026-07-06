package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func testUploadsHandler(t *testing.T) *UploadsHandler {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	h, err := NewUploadsHandler(log, UploadsConfig{
		Bucket:        "test-bucket",
		KeyPrefix:     "uploads/",
		PublicBaseURL: "https://data.example.io",
		Endpoint:      "https://acct.r2.cloudflarestorage.com",
		AccessKeyID:   "key",
		SecretKey:     "secret",
	})
	if err != nil {
		t.Fatalf("NewUploadsHandler: %v", err)
	}

	return h
}

func TestNewUploadsHandlerRequiresCoreFields(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	if _, err := NewUploadsHandler(log, UploadsConfig{Endpoint: "x", PublicBaseURL: "y"}); err == nil {
		t.Fatal("expected error when bucket is missing")
	}
}

func TestNewUploadsHandlerDefaultsMaxObjectBytes(t *testing.T) {
	h := testUploadsHandler(t)
	if h.cfg.MaxObjectBytes != DefaultMaxObjectBytes {
		t.Fatalf("MaxObjectBytes = %d, want %d", h.cfg.MaxObjectBytes, DefaultMaxObjectBytes)
	}
}

func TestUploadsHandlerRejectsUnknownMethod(t *testing.T) {
	h := testUploadsHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/uploads", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestUploadsHandlerDeleteRequiresKey(t *testing.T) {
	h := testUploadsHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/uploads", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadsHandlerDeleteRejectsKeyOutsidePrefix(t *testing.T) {
	h := testUploadsHandler(t)

	for _, key := range []string{"other/file.html", "uploads/../secrets.txt", "/uploads/x"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/uploads?key="+key, nil))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q: status = %d, want 400", key, rec.Code)
		}
	}
}

func TestUploadsHandlerRejectsEmptyBody(t *testing.T) {
	h := testUploadsHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/uploads?name=x.txt", strings.NewReader("")))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSanitizeUploadName(t *testing.T) {
	cases := map[string]string{
		"report.md":                       "report.md",
		"../../etc/passwd":                "passwd",
		"my chart (final).png":            "my_chart__final_.png",
		"":                                "file",
		".":                               "file",
		"..":                              "file",
		"/":                               "file",
		"a/b/c.txt":                       "c.txt",
		"...":                             "file",
		"héllo.png":                       "h_llo.png",
		strings.Repeat("x", 200) + ".png": strings.Repeat("x", 124) + ".png",
	}

	for in, want := range cases {
		if got := sanitizeUploadName(in); got != want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInlineSafe(t *testing.T) {
	inline := []string{"image/png", "image/jpeg", "application/pdf", "text/plain; charset=utf-8", "video/mp4"}
	forced := []string{"text/html", "image/svg+xml", "application/xhtml+xml", "text/javascript", "application/octet-stream"}

	for _, ct := range inline {
		if !inlineSafe(ct) {
			t.Errorf("inlineSafe(%q) = false, want true", ct)
		}
	}

	for _, ct := range forced {
		if inlineSafe(ct) {
			t.Errorf("inlineSafe(%q) = true, want false", ct)
		}
	}
}

func TestForceDownload(t *testing.T) {
	off := &UploadsHandler{cfg: UploadsConfig{RenderHTML: false}}
	on := &UploadsHandler{cfg: UploadsConfig{RenderHTML: true}}

	// Inline-safe types never download, regardless of RenderHTML.
	for _, ct := range []string{"image/png", "application/pdf", "text/plain"} {
		if off.forceDownload(ct) || on.forceDownload(ct) {
			t.Errorf("forceDownload(%q) = true, want false", ct)
		}
	}

	// HTML downloads by default and renders inline only when RenderHTML is on.
	for _, ct := range []string{"text/html", "text/html; charset=utf-8"} {
		if !off.forceDownload(ct) {
			t.Errorf("forceDownload(%q) with RenderHTML off = false, want true", ct)
		}
		if on.forceDownload(ct) {
			t.Errorf("forceDownload(%q) with RenderHTML on = true, want false", ct)
		}
	}

	// SVG and other scriptable markup stay download-only even with RenderHTML on.
	for _, ct := range []string{"image/svg+xml", "text/javascript", "application/xhtml+xml"} {
		if !on.forceDownload(ct) {
			t.Errorf("forceDownload(%q) with RenderHTML on = false, want true", ct)
		}
	}
}
