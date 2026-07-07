package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestUploadStoreEvictsOnTotalBytes(t *testing.T) {
	// High item cap, low byte cap → only the byte bound can trigger eviction.
	store := &uploadStore{items: make(map[string]*uploadItem), maxItems: 100, maxTotalBytes: 10}

	first := store.put("a.bin", "application/octet-stream", make([]byte, 6))
	store.put("b.bin", "application/octet-stream", make([]byte, 6)) // total 12 > 10

	if _, ok := store.get(first); ok {
		t.Fatal("oldest item should have been evicted on the total-bytes bound")
	}

	if store.totalBytes > store.maxTotalBytes {
		t.Fatalf("totalBytes = %d, want <= %d", store.totalBytes, store.maxTotalBytes)
	}
}

func TestUploadStoreKeepsItemLargerThanTotalCap(t *testing.T) {
	// A single item over the byte cap is kept, not evicted into an empty store.
	store := &uploadStore{items: make(map[string]*uploadItem), maxItems: 100, maxTotalBytes: 5}

	id := store.put("big.bin", "application/octet-stream", make([]byte, 9))
	if _, ok := store.get(id); !ok {
		t.Fatal("the only item must be retained even if it exceeds the byte cap")
	}
}

func TestUploadStoreSweepFreesIdlePreviews(t *testing.T) {
	store := newUploadStore()

	stale := store.put("stale.txt", "text/plain", make([]byte, 4))
	fresh := store.put("fresh.txt", "text/plain", make([]byte, 4))
	store.items[stale].lastAccess = time.Now().Add(-2 * uploadTTL)

	store.sweep(time.Now())

	if _, ok := store.items[stale]; ok {
		t.Fatal("idle preview should have been swept")
	}

	if _, ok := store.items[fresh]; !ok {
		t.Fatal("fresh preview must survive the sweep")
	}

	if store.totalBytes != 4 {
		t.Fatalf("totalBytes = %d, want 4", store.totalBytes)
	}
}

func TestUploadStoreGetRefreshesTTL(t *testing.T) {
	store := newUploadStore()

	id := store.put("kept.txt", "text/plain", make([]byte, 4))
	store.items[id].lastAccess = time.Now().Add(-2 * uploadTTL)

	// An access (e.g. the preview page being reloaded) resets the idle clock.
	if _, ok := store.get(id); !ok {
		t.Fatal("item should still be present before the sweep")
	}

	store.sweep(time.Now())

	if _, ok := store.items[id]; !ok {
		t.Fatal("recently accessed preview must survive the sweep")
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

func TestServeRawMirrorsWorkerCSP(t *testing.T) {
	cases := []struct {
		name, contentType, wantCSP string
	}{
		// HTML previews like published: scripted opaque origin, but the local
		// origin's APIs must stay unreachable (connect-src/form-action 'none').
		{"page.html", "text/html; charset=utf-8", "sandbox allow-scripts allow-downloads; connect-src 'none'; form-action 'none'"},
		{"pic.svg", "image/svg+xml", "sandbox"},
		{"chart.png", "image/png", ""},
		{"notes.txt", "text/plain", ""},
	}

	svc := testUploadService()
	r := chi.NewRouter()
	r.Get("/u/{id}/raw", svc.handleUploadServeRaw)

	for _, tc := range cases {
		id := svc.uploads.put(tc.name, tc.contentType, []byte("<script>alert(1)</script>"))

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/u/"+id+"/raw", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.name, rec.Code)
		}

		if got := rec.Header().Get("Content-Security-Policy"); got != tc.wantCSP {
			t.Errorf("%s: CSP = %q, want %q", tc.name, got, tc.wantCSP)
		}

		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: nosniff header = %q", tc.name, got)
		}
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

func TestDisableUploadsGatesAllRoutes(t *testing.T) {
	svc := testUploadService()
	off := false
	svc.cfg.Uploads = &off

	r := chi.NewRouter()
	svc.mountAPIRoutes(r)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/uploads?name=x.txt", strings.NewReader("hi")),
		httptest.NewRequest(http.MethodPost, "/api/v1/uploads/publish", strings.NewReader(`{"id":"x"}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/published?key=panda/uploads/x", nil),
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", req.Method, req.URL.Path, rec.Code)
		}

		if !strings.Contains(rec.Body.String(), "server.uploads") {
			t.Errorf("%s: body should name the config knob: %s", req.URL.Path, rec.Body.String())
		}
	}
}
