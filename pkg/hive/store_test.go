package hive

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
)

// suiteFile writes a suite the way hive does: run metadata before the tests.
type suiteFile struct {
	Name           string         `json:"name"`
	RunMetadata    *RunMetadata   `json:"runMetadata"`
	TestCases      map[string]any `json:"testCases"`
	TestDetailsLog string         `json:"testDetailsLog"`
}

func testCase(name string, pass bool) map[string]any {
	return map[string]any{
		"name": name,
		"description": `<a href="https://github.com/ethereum/execution-specs/blob/abc/tests/t.py#L1">[source]</a>` +
			`<br/>./hive --sim eels/consume-engine --client reth`,
		"summaryResult": map[string]any{"pass": pass, "details": "boom", "log": map[string]any{"begin": 0, "end": 6}},
		"clientInfo": map[string]any{
			"c1": map[string]any{
				"name":       "reth_default",
				"logFile":    "reth.log",
				"logOffsets": map[string]any{"begin": 6, "end": 12},
			},
		},
	}
}

// fakeHive serves group tg, whose latest batch has a failing reth run and a
// passing besu run and whose previous batch has an older reth run, and
// mainnet, whose results live under /generic/ as on hive.ethpandaops.io.
func fakeHive(t *testing.T) *httptest.Server {
	t.Helper()

	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	runs := []Run{
		{
			Name: "eels/consume-engine", NTests: 3, Passes: 2, Fails: 1,
			Clients: []string{"reth_default"}, Versions: map[string]string{"reth_default": "reth Version: 2.9.0+abcdef1"},
			Start: now, FileName: "suite-reth.json",
		},
		{
			Name: "eels/consume-engine", NTests: 3, Passes: 3,
			Clients: []string{"besu_default"}, Versions: map[string]string{"besu_default": "besu/v26.10-develop-1234567/linux"},
			Start: now, FileName: "suite-besu.json",
		},
		{
			Name: "eels/consume-engine", NTests: 3, Passes: 1, Fails: 2,
			Clients: []string{"reth_default"}, Versions: map[string]string{"reth_default": "reth Version: 2.8.0+abcdef0"},
			Start: now.Add(-24 * time.Hour), FileName: "suite-reth-old.json",
		},
	}

	meta := &RunMetadata{HiveCommand: []string{"./hive", "--sim.buildarg", "fixtures=https://example.com/fixtures.tar.gz"}}
	suites := map[string]suiteFile{
		"suite-reth.json": {Name: "eels/consume-engine", RunMetadata: meta, TestDetailsLog: "details.log", TestCases: map[string]any{
			"1": testCase("tests/a.py::test_kept_failing[fork_Amsterdam-engine]-reth_default", false),
			"2": testCase("tests/a.py::test_new_failure[fork_Amsterdam-engine]-reth_default", true),
			"3": testCase("tests/a.py::test_passing[fork_Amsterdam-engine]-reth_default", true),
		}},
		"suite-besu.json": {Name: "eels/consume-engine", RunMetadata: meta, TestCases: map[string]any{
			"1": testCase("tests/a.py::test_passing[fork_Amsterdam-engine]-besu_default", true),
		}},
		"suite-reth-old.json": {Name: "eels/consume-engine", RunMetadata: meta, TestCases: map[string]any{
			"1": testCase("tests/a.py::test_kept_failing[fork_Amsterdam-engine]-reth_default", false),
			"2": testCase("tests/a.py::test_fixed[fork_Amsterdam-engine]-reth_default", false),
		}},
	}
	// test_new_failure fails in the latest run.
	suites["suite-reth.json"].TestCases["2"] = testCase("tests/a.py::test_new_failure[fork_Amsterdam-engine]-reth_default", false)
	runs[0].Fails, runs[0].Passes = 2, 1

	var srv *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/discovery.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"name": "tg", "address": srv.URL + "/tg/"},
			{"name": "mainnet", "address": srv.URL + "/generic/"},
		})
	})
	mux.HandleFunc("/tg/listing.jsonl", func(w http.ResponseWriter, _ *http.Request) {
		for _, run := range runs {
			line, _ := json.Marshal(run)
			_, _ = w.Write(append(line, '\n'))
		}
	})
	mux.HandleFunc("/generic/listing.jsonl", func(w http.ResponseWriter, _ *http.Request) {
		line, _ := json.Marshal(runs[1])
		_, _ = w.Write(append(line, '\n'))
	})
	mux.HandleFunc("/tg/results/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/tg/results/")

		var data []byte

		switch suite, ok := suites[name]; {
		case ok:
			data, _ = json.Marshal(suite)
		case name == "details.log":
			data = []byte("DETAIL")
		case name == "reth.log":
			data = []byte("start CLIENT end")
		default:
			http.NotFound(w, r)

			return
		}

		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	srv := fakeHive(t)

	return NewStore(srv.URL, srv.Client())
}

func TestGroupsFollowDiscoveryAddresses(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	groups, err := store.Groups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"tg", "mainnet"}, groups)

	// mainnet's listing is under /generic/, not /mainnet/.
	summary, err := store.Summary(ctx, "mainnet")
	require.NoError(t, err)
	require.Contains(t, summary.Clients, "besu")

	_, err = store.Summary(ctx, "nope")
	require.ErrorIs(t, err, ErrUnknownGroup)
}

func TestSummaryCountsTheLatestRunPerClient(t *testing.T) {
	summary, err := newTestStore(t).Summary(context.Background(), "tg")
	require.NoError(t, err)

	require.Equal(t, 2, summary.Clients["reth"].Fail)
	require.Equal(t, "v2.9.0+abcdef1", summary.Clients["reth"].Version)
	require.Equal(t, 3, summary.Clients["besu"].Pass)
	require.Equal(t, 6, summary.Simulators["consume-engine"].Total)
}

func TestFailuresIncludeFixturesAndRates(t *testing.T) {
	failures, err := newTestStore(t).Failures(context.Background(), "tg", Filter{})
	require.NoError(t, err)

	require.Equal(t, 2, failures.TotalFails)
	require.NotContains(t, failures.Clients, "besu")

	sim := failures.Clients["reth"].Simulators["consume-engine"]
	require.Equal(t, "https://example.com/fixtures.tar.gz", sim.FixturesRelease)
	require.InDelta(t, 66.7, sim.FailRate, 0.1)
}

func TestTestsFilterAndLinkEachTest(t *testing.T) {
	store := newTestStore(t)

	failing, err := store.Tests(context.Background(), "tg", Filter{Status: "fail"}, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 2, failing.Total)

	first := failing.Tests[0]
	require.Equal(t, "tests/a.py::test_kept_failing[fork_Amsterdam-engine]", first.PytestID)
	require.Equal(t, "Amsterdam", first.Fork)
	require.Equal(t, "https://github.com/ethereum/execution-specs/blob/abc/tests/t.py#L1", first.SourceURL)
	require.Equal(t, store.baseURL+"/#/test/tg/suite-reth?testnumber=1", first.HiveURL)

	matched, err := store.Tests(context.Background(), "tg", Filter{Status: "", Pattern: "PASSING", Client: "besu"}, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 1, matched.Total)

	page, err := store.Tests(context.Background(), "tg", Filter{Status: "fail"}, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Len(t, page.Tests, 1)
	require.Equal(t, "test_new_failure", page.Tests[0].Function)
}

func TestDetailFetchesOnlyThatTestAndItsLogs(t *testing.T) {
	detail, err := newTestStore(t).Detail(context.Background(), "tg", "suite-reth.json", "1", false)
	require.NoError(t, err)

	require.Equal(t, "boom", detail.Error)
	require.Equal(t, "DETAIL", detail.DetailLog)
	require.Equal(t, "CLIENT", detail.ClientLog)
	require.Equal(t, "reth", detail.Client)
	require.Equal(t, "uv run fill tests/a.py::test_kept_failing[fork_Amsterdam-engine]", detail.FillCommand)
	require.Equal(t, "./hive --sim eels/consume-engine --client reth", detail.HiveCommand)

	_, err = newTestStore(t).Detail(context.Background(), "tg", "suite-reth.json", "99", false)
	require.ErrorIs(t, err, ErrTestNotFound)
}

func TestDiffSortsFailuresBetweenBatches(t *testing.T) {
	store := newTestStore(t)

	diff, err := store.Diff(context.Background(), "tg", false)
	require.NoError(t, err)

	require.Equal(t, 1, diff.Regressions["reth"].Total)
	require.Equal(t, 1, diff.Fixes["reth"].Total)
	require.Equal(t, 1, diff.Unchanged["reth"].Total)
	require.Empty(t, diff.Regressions["reth"].Simulators["consume-engine"].Tests)

	withTests, err := store.Diff(context.Background(), "tg", true)
	require.NoError(t, err)
	require.Equal(t, "test_new_failure", withTests.Regressions["reth"].Simulators["consume-engine"].Tests[0].Function)
}

func TestReadSuiteStopsAtRunMetadata(t *testing.T) {
	// Anything after the run metadata is never read, so it may be invalid.
	input := `{"name":"sim","runMetadata":{"hiveCommand":["./hive"]},"testCases":{not json`

	suite, err := readSuite(strings.NewReader(input), false, newInterner().get)
	require.NoError(t, err)
	require.False(t, suite.HasTests)
	require.Equal(t, []string{"./hive"}, suite.RunMetadata.HiveCommand)
}

func TestReadSuiteSharesNamesAcrossClients(t *testing.T) {
	suiteFor := func(client string) string {
		return `{"testCases":{"7":{"name":"m.py::test_x[fork_Osaka]-` + client + `","summaryResult":{"pass":false}}}}`
	}

	names := newInterner()

	reth, err := readSuite(strings.NewReader(suiteFor("reth_default")), true, names.get)
	require.NoError(t, err)

	besu, err := readSuite(strings.NewReader(suiteFor("besu_default")), true, names.get)
	require.NoError(t, err)

	a, b := reth.Tests[0].Name, besu.Tests[0].Name
	require.Equal(t, "m.py::test_x[fork_Osaka]", a)
	require.Equal(t, unsafe.StringData(a), unsafe.StringData(b), "one shared copy of the name")
}

func TestParseHelpers(t *testing.T) {
	require.Equal(t, "go-ethereum", clientName("go-ethereum_default"))
	require.Equal(t, "reth", clientName("reth_default"))
	require.Equal(t, "consume-engine", simulatorName("eels/consume-engine"))
	require.Equal(t, "BPO2ToAmsterdamAtTime15k", forkOf("m::f[fork_BPO2ToAmsterdamAtTime15k-blockchain_test]"))
	require.Equal(t, "Osaka", forkOf("m::f[fork_Osaka]"))
	require.Empty(t, sourceURL(`<a href="https://evil.example/x">[source]</a>`))
	require.Equal(t, "v1.17.11-unstable-a0cba163", cleanVersion("go-ethereum", "Geth/v1.17.11-unstable-a0cba163-20261029/linux-amd64/go1.25"))
}
