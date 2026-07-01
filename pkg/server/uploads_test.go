package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

func testUploadService() *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{log: log, uploads: newUploadStore()}
}

func TestUploadStoreEvictsOldest(t *testing.T) {
	store := newUploadStore()

	first := store.put("first.txt", "text/plain", []byte("a"))
	for i := 0; i < uploadMaxItems; i++ {
		store.put("f.txt", "text/plain", []byte("x"))
	}

	if _, ok := store.get(first); ok {
		t.Fatal("oldest item should have been evicted")
	}

	if len(store.items) != uploadMaxItems {
		t.Fatalf("store size = %d, want %d", len(store.items), uploadMaxItems)
	}
}

func TestHandleUploadStoresPrivately(t *testing.T) {
	svc := testUploadService()

	rec := httptest.NewRecorder()
	svc.handleUpload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/uploads?name=report.html", strings.NewReader("<h1>hi</h1>")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), `"preview_path":"/u/`) {
		t.Fatalf("response missing preview_path: %s", rec.Body.String())
	}

	if len(svc.uploads.items) != 1 {
		t.Fatalf("store size = %d, want 1", len(svc.uploads.items))
	}
}

func TestHandleUploadRejectsEmpty(t *testing.T) {
	svc := testUploadService()

	rec := httptest.NewRecorder()
	svc.handleUpload(rec, httptest.NewRequest(http.MethodPost, "/api/v1/uploads?name=x.txt", strings.NewReader("")))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeRawSetsSandboxCSP(t *testing.T) {
	svc := testUploadService()
	id := svc.uploads.put("page.html", "text/html; charset=utf-8", []byte("<script>alert(1)</script>"))

	r := chi.NewRouter()
	r.Get("/u/{id}/raw", svc.handleUploadServeRaw)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/u/"+id+"/raw", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("CSP = %q, want sandbox", got)
	}

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header = %q", got)
	}
}

func TestPublishRequiresID(t *testing.T) {
	svc := testUploadService()

	rec := httptest.NewRecorder()
	svc.handleUploadPublish(rec, httptest.NewRequest(http.MethodPost, "/api/v1/uploads/publish", strings.NewReader(`{}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPublishUnknownID(t *testing.T) {
	svc := testUploadService()

	rec := httptest.NewRecorder()
	svc.handleUploadPublish(rec, httptest.NewRequest(http.MethodPost, "/api/v1/uploads/publish", strings.NewReader(`{"id":"nope"}`)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
