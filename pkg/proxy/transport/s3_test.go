package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestS3HandlerRejectsOversizedBodies(t *testing.T) {
	t.Parallel()

	handler := NewS3Handler(testLogger(), &S3Config{
		Endpoint:  "https://s3.example.com",
		AccessKey: "access-key",
		SecretKey: "secret-key",
		Bucket:    "artifacts",
		Region:    "us-east-1",
	})

	req := httptest.NewRequest(http.MethodPut, "http://proxy.test/s3/artifacts/file.txt", bytes.NewReader(bytes.Repeat([]byte("a"), 100*1024*1024+1)))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("body = %q, want oversized body error", rec.Body.String())
	}
}
