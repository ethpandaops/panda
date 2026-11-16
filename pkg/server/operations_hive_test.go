package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/hive"
	"github.com/ethpandaops/panda/pkg/operations"
)

// newHiveOperationService returns a service whose hive store reads a fake
// hive with one group, tg, holding one failing reth run.
func newHiveOperationService(t *testing.T) *service {
	t.Helper()

	var srv *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/discovery.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "tg", "address": srv.URL + "/tg/"}})
	})
	mux.HandleFunc("/tg/listing.jsonl", func(w http.ResponseWriter, _ *http.Request) {
		line, _ := json.Marshal(hive.Run{
			Name: "eels/consume-engine", NTests: 2, Passes: 1, Fails: 1,
			Clients:  []string{"reth_default"},
			Versions: map[string]string{"reth_default": "reth Version: 2.9.0+abcdef1"},
			Start:    time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), FileName: "suite-reth.json",
		})
		_, _ = w.Write(append(line, '\n'))
	})
	mux.HandleFunc("/tg/results/suite-reth.json", func(w http.ResponseWriter, r *http.Request) {
		data := []byte(`{"runMetadata":{"hiveCommand":["./hive"]},"testCases":{` +
			`"1":{"name":"tests/a.py::test_fails[fork_Amsterdam-engine]-reth_default","summaryResult":{"pass":false,"details":"boom"}},` +
			`"2":{"name":"tests/a.py::test_passes[fork_Amsterdam-engine]-reth_default","summaryResult":{"pass":true}}}}`)
		http.ServeContent(w, r, "suite-reth.json", time.Time{}, bytes.NewReader(data))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{log: log, hiveInst: hive.NewStore(srv.URL, srv.Client())}
}

func runHiveOperation(t *testing.T, svc *service, operationID string, args map[string]any) (bool, *httptest.ResponseRecorder) {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/operations/hive", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()

	return svc.handleHiveOperation(operationID, rec, req), rec
}

func TestHiveOperationsReturnObjects(t *testing.T) {
	svc := newHiveOperationService(t)

	handled, rec := runHiveOperation(t, svc, "hive.list_tests", map[string]any{"group": "tg"})
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var response struct {
		Kind string        `json:"kind"`
		Data hive.TestList `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, operations.ResultKindObject, response.Kind)
	require.Equal(t, 1, response.Data.Total, "only the failing test by default")
	require.Equal(t, "test_fails", response.Data.Tests[0].Function)
	require.Contains(t, response.Data.Tests[0].HiveURL, "/#/test/tg/suite-reth?testnumber=1")

	handled, rec = runHiveOperation(t, svc, "hive.list_tests", map[string]any{"group": "tg", "status": "all"})
	require.True(t, handled)
	require.Contains(t, rec.Body.String(), `"total":2`)
}

func TestHiveOperationErrors(t *testing.T) {
	svc := newHiveOperationService(t)

	handled, _ := runHiveOperation(t, svc, "dora.get_slot", map[string]any{})
	require.False(t, handled, "other modules' operations are not handled")

	_, rec := runHiveOperation(t, svc, "hive.get_summary", map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code)

	_, rec = runHiveOperation(t, svc, "hive.get_summary", map[string]any{"group": "nope"})
	require.Equal(t, http.StatusNotFound, rec.Code)

	_, rec = runHiveOperation(t, svc, "hive.get_test", map[string]any{
		"group": "tg", "suite_file": "suite-reth.json", "test_id": "99",
	})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHiveGetTestReadsOneTest(t *testing.T) {
	svc := newHiveOperationService(t)

	_, rec := runHiveOperation(t, svc, "hive.get_test", map[string]any{
		"group": "tg", "suite_file": "suite-reth.json", "test_id": "1",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var response struct {
		Data hive.TestDetail `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, "boom", response.Data.Error)
	require.Equal(t, "uv run fill tests/a.py::test_fails[fork_Amsterdam-engine]", response.Data.FillCommand)
}
