// Package hive reads the hive test results that ethPandaOps CI publishes at
// https://hive.ethpandaops.io, keeping only what queries need.
//
// A suite file holds every test of one run and can be tens of MB, mostly
// test descriptions. The store keeps, per test, its number, name, result,
// source link and where it sits in the file, and fetches a single test's
// bytes by range when its detail is asked for. Runs without failures are
// read only up to their run metadata until a query needs their tests.
package hive

import "time"

// Run is one line of a group's listing.jsonl: one suite run for one client.
type Run struct {
	Name     string            `json:"name"`
	NTests   int               `json:"ntests"`
	Passes   int               `json:"passes"`
	Fails    int               `json:"fails"`
	Timeout  bool              `json:"timeout"`
	Clients  []string          `json:"clients"`
	Versions map[string]string `json:"versions"`
	Start    time.Time         `json:"start"`
	FileName string            `json:"fileName"`
	Size     int64             `json:"size"`
	SimLog   string            `json:"simLog"`
}

// Suite is what the store keeps of a suite file.
type Suite struct {
	RunMetadata    *RunMetadata
	TestDetailsLog string
	Tests          []SuiteTest // sorted by ID
	// HasTests is false when only the run metadata at the top of the file
	// was read.
	HasTests bool
}

// SuiteTest is one test case of a suite. Name and Source are interned, so a
// test that several clients ran is held once.
type SuiteTest struct {
	Name   string // pytest ID, without the client suffix
	Source string // the test's [source] link
	Offset int64  // where the test case is in the suite file
	Size   int32
	ID     uint32
	Pass   bool
}

// TestCase is one test case in full, as the suite file stores it.
type TestCase struct {
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	SummaryResult SummaryResult         `json:"summaryResult"`
	ClientInfo    map[string]ClientInfo `json:"clientInfo"`
}

// SummaryResult is a test case's outcome and where its log is.
type SummaryResult struct {
	Pass    bool      `json:"pass"`
	Details string    `json:"details,omitempty"`
	Log     *LogRange `json:"log,omitempty"`
}

// LogRange is a byte range in a log file.
type LogRange struct {
	Begin int64 `json:"begin"`
	End   int64 `json:"end"`
}

// ClientInfo is a client container a test case ran against.
type ClientInfo struct {
	Name       string    `json:"name"`
	LogFile    string    `json:"logFile"`
	LogOffsets *LogRange `json:"logOffsets,omitempty"`
}

// RunMetadata is how a suite run was started.
type RunMetadata struct {
	HiveCommand  []string      `json:"hiveCommand"`
	ClientConfig *ClientConfig `json:"clientConfig,omitempty"`
}

// ClientConfig is the hive client file a run used.
type ClientConfig struct {
	Content *struct {
		Clients []struct {
			Client    string            `json:"client"`
			BuildArgs map[string]string `json:"build_args"`
		} `json:"clients"`
	} `json:"content,omitempty"`
}

// Summary is a group's pass and fail counts per client and simulator.
type Summary struct {
	Group      string                      `json:"group"`
	LastRun    time.Time                   `json:"last_run"`
	Clients    map[string]*ClientSummary   `json:"clients"`
	Simulators map[string]*SimulatorCounts `json:"simulators"`
}

// ClientSummary is one client's counts in a group.
type ClientSummary struct {
	Version string `json:"version"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Pass    int    `json:"pass"`
	Fail    int    `json:"fail"`
	Timeout int    `json:"timeout"`
}

// SimulatorCounts is one simulator's counts in a group.
type SimulatorCounts struct {
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Total int `json:"total"`
}

// Failures is a group's failures per client and simulator.
type Failures struct {
	Group      string                     `json:"group"`
	TotalFails int                        `json:"total_fails"`
	FailRate   float64                    `json:"fail_rate"`
	Clients    map[string]*ClientFailures `json:"clients"`
}

// ClientFailures is one client's failures in a group.
type ClientFailures struct {
	Version    string                        `json:"version"`
	Repo       string                        `json:"repo,omitempty"`
	Branch     string                        `json:"branch,omitempty"`
	Commit     string                        `json:"commit,omitempty"`
	TotalFails int                           `json:"total_fails"`
	FailRate   float64                       `json:"fail_rate"`
	Simulators map[string]*SimulatorFailures `json:"simulators"`
}

// SimulatorFailures is one client's failures with one simulator.
type SimulatorFailures struct {
	RunDate         time.Time `json:"run_date"`
	SuiteFile       string    `json:"suite_file"`
	FixturesRelease string    `json:"fixtures_release,omitempty"`
	TotalFails      int       `json:"total_fails"`
	FailRate        float64   `json:"fail_rate"`
	TotalTests      int       `json:"total_tests"`
}

// Test is one test of a run, for lists, search and diff.
type Test struct {
	Client    string `json:"client"`
	Simulator string `json:"simulator"`
	Module    string `json:"module"`
	Function  string `json:"function"`
	PytestID  string `json:"pytest_id"`
	Fork      string `json:"fork,omitempty"`
	Pass      bool   `json:"pass"`
	SuiteFile string `json:"suite_file"`
	TestID    string `json:"test_id"`
	SourceURL string `json:"source_url,omitempty"`
	HiveURL   string `json:"hive_url"`
}

// TestList is a page of tests and how many matched.
type TestList struct {
	Group string `json:"group"`
	Total int    `json:"total"`
	Tests []Test `json:"tests"`
}

// TestDetail is one test with its error, logs and reproduce commands.
type TestDetail struct {
	Test
	FillCommand    string `json:"fill_command"`
	ConsumeCommand string `json:"consume_command,omitempty"`
	HiveCommand    string `json:"hive_command,omitempty"`
	Error          string `json:"error,omitempty"`
	DetailLog      string `json:"detail_log,omitempty"`
	ClientLog      string `json:"client_log,omitempty"`
}

// Diff is what changed between a group's latest two batches of runs.
type Diff struct {
	Group       string                 `json:"group"`
	FromRun     time.Time              `json:"from_run"`
	ToRun       time.Time              `json:"to_run"`
	Regressions map[string]*ClientDiff `json:"regressions"`
	Fixes       map[string]*ClientDiff `json:"fixes"`
	Unchanged   map[string]*ClientDiff `json:"unchanged_fails"`
}

// ClientDiff is one client's share of a diff section.
type ClientDiff struct {
	Total      int                       `json:"total"`
	Simulators map[string]*SimulatorDiff `json:"simulators"`
}

// SimulatorDiff is one simulator's share of a client's diff section. Tests
// is left out unless the diff was asked for with tests.
type SimulatorDiff struct {
	Total int    `json:"total"`
	Tests []Test `json:"tests,omitempty"`
}
