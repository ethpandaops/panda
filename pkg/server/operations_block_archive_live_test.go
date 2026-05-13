//go:build live

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	blockarchive "github.com/ethpandaops/panda/modules/block_archive"
	"github.com/ethpandaops/panda/pkg/module"
)

// live-only: hits the public production block-archiver.
//
//	go test -tags=live -run TestBlockArchiveLive_ListNetworks ./pkg/server/...
func TestBlockArchiveLive_ListNetworks(t *testing.T) {
	svc := newLiveBlockArchiveService(t)

	body := callOp(t, svc, "block_archive.list_networks", map[string]any{"active": true})

	var payload struct {
		Kind string `json:"kind"`
		Data struct {
			Networks []struct {
				Name    string `json:"name"`
				Status  string `json:"status"`
				Source  string `json:"source"`
				Polling bool   `json:"polling"`
			} `json:"networks"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response: %v\nraw: %s", err, body)
	}

	if payload.Kind != "object" {
		t.Fatalf("unexpected kind %q", payload.Kind)
	}

	names := make(map[string]bool, len(payload.Data.Networks))
	for _, n := range payload.Data.Networks {
		names[n.Name] = true
		if !n.Polling {
			t.Errorf("active=true should only return polling networks, got %+v", n)
		}
	}

	t.Logf("active networks: %d (%v)", len(payload.Data.Networks), names)
	for _, want := range []string{"mainnet", "sepolia", "hoodi"} {
		if !names[want] {
			t.Errorf("expected %q in active networks, got %v", want, names)
		}
	}
}

func TestBlockArchiveLive_ListNetworks_All(t *testing.T) {
	svc := newLiveBlockArchiveService(t)

	body := callOp(t, svc, "block_archive.list_networks", nil)

	var payload struct {
		Data struct {
			Networks []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"networks"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(payload.Data.Networks) < 4 {
		t.Errorf("expected at least 4 networks (3 static + ≥1 devnet), got %d", len(payload.Data.Networks))
	}

	hasStatic := false
	hasCartographoor := false
	for _, n := range payload.Data.Networks {
		if n.Source == "static" {
			hasStatic = true
		}
		if n.Source == "cartographoor" {
			hasCartographoor = true
		}
	}
	if !hasStatic || !hasCartographoor {
		t.Errorf("expected both source=static and source=cartographoor entries")
	}
}

func TestBlockArchiveLive_DownloadSSZ_404(t *testing.T) {
	svc := newLiveBlockArchiveService(t)

	args := map[string]any{
		"network":    "mainnet",
		"slot":       1,
		"block_root": "0x" + strings.Repeat("0", 64),
	}

	rec := newRecorder()
	r := newOpRequest(t, args)
	if !svc.handleBlockArchiveOperation("block_archive.download_ssz", rec, r) {
		t.Fatal("handler did not handle operation")
	}

	if rec.Code != http.StatusNotFound {
		t.Logf("status=%d body=%s", rec.Code, rec.Body.String())
		if rec.Code >= 500 {
			t.Errorf("unexpected 5xx: %d", rec.Code)
		}
	}
}

func newLiveBlockArchiveService(t *testing.T) *service {
	t.Helper()

	mod := blockarchive.New()
	if err := mod.Init(nil); err != nil {
		t.Fatalf("init module: %v", err)
	}
	if err := mod.Validate(); err != nil {
		t.Fatalf("validate module: %v", err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	reg := module.NewRegistry(logger)
	reg.Add(mod)
	if err := reg.InitModule("block_archive", nil); err != nil {
		t.Fatalf("registry init: %v", err)
	}
	if err := mod.Start(context.Background()); err != nil {
		t.Fatalf("start module: %v", err)
	}

	return &service{
		log:            logger,
		moduleRegistry: reg,
		httpClient:     &http.Client{},
	}
}

func callOp(t *testing.T, svc *service, operationID string, args map[string]any) []byte {
	t.Helper()

	rec := newRecorder()
	r := newOpRequest(t, args)
	if !svc.handleBlockArchiveOperation(operationID, rec, r) {
		t.Fatalf("handler did not handle %q", operationID)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("op %q non-200 status %d: %s", operationID, rec.Code, rec.Body.String())
	}

	return rec.Body.Bytes()
}

func newOpRequest(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(map[string]any{"args": args})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/operations/block_archive", io.NopCloser(bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
