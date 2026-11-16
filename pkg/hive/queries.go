package hive

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// batchWindow groups a listing's runs into batches: runs that started within
// this long of each other belong to one CI run of a group.
const batchWindow = 4 * time.Hour

// Filter narrows the tests a query looks at.
type Filter struct {
	Client    string // hive client name, such as reth or go-ethereum
	Simulator string // short simulator name, such as consume-engine
	Status    string // "fail", "pass" or "" for both
	Pattern   string // case-insensitive regexp on the pytest ID
}

func (f Filter) matchRun(run Run) bool {
	client, _ := firstClient(run)

	return (f.Client == "" || client == f.Client) &&
		(f.Simulator == "" || simulatorName(run.Name) == f.Simulator)
}

func (f Filter) matchTest(t SuiteTest, re *regexp.Regexp) bool {
	switch {
	case f.Status == "fail" && t.Pass, f.Status == "pass" && !t.Pass:
		return false
	case re != nil && !re.MatchString(t.Name):
		return false
	default:
		return true
	}
}

func (f Filter) regexp() (*regexp.Regexp, error) {
	if f.Pattern == "" {
		return nil, nil
	}

	if len(f.Pattern) > 1024 {
		return nil, fmt.Errorf("filter pattern too long")
	}

	re, err := regexp.Compile("(?i)" + f.Pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid filter: %w", err)
	}

	return re, nil
}

// Summary returns a group's pass and fail counts per client and simulator.
func (s *Store) Summary(ctx context.Context, group string) (*Summary, error) {
	l, err := s.listing(ctx, group)
	if err != nil {
		return nil, err
	}

	suites := s.readSuites(ctx, group, l.latest, func(Run) bool { return false })
	out := &Summary{
		Group:      group,
		Clients:    make(map[string]*ClientSummary),
		Simulators: make(map[string]*SimulatorCounts),
	}

	for _, run := range l.latest {
		sim := simulatorName(run.Name)

		counts := out.Simulators[sim]
		if counts == nil {
			counts = &SimulatorCounts{}
			out.Simulators[sim] = counts
		}

		counts.Pass += run.Passes
		counts.Fail += run.Fails
		counts.Total += run.NTests

		for _, key := range run.Clients {
			name := clientName(key)

			client := out.Clients[name]
			if client == nil {
				client = &ClientSummary{
					Version: cleanVersion(name, run.Versions[key]),
					Commit:  parseCommit(name, run.Versions[key]),
				}
				if suite := suites[run.FileName]; suite != nil {
					client.Repo, client.Branch = buildInfo(suite.RunMetadata, name)
				}

				out.Clients[name] = client
			}

			client.Pass += run.Passes
			client.Fail += run.Fails

			if run.Timeout {
				client.Timeout += run.NTests
			}
		}

		if run.Start.After(out.LastRun) {
			out.LastRun = run.Start
		}
	}

	return out, nil
}

// Failures returns a group's failures and fail rates per client and
// simulator, from the latest run of each.
func (s *Store) Failures(ctx context.Context, group string, filter Filter) (*Failures, error) {
	l, err := s.listing(ctx, group)
	if err != nil {
		return nil, err
	}

	var runs []Run

	for _, run := range l.latest {
		if run.Fails > 0 && filter.matchRun(run) {
			runs = append(runs, run)
		}
	}

	suites := s.readSuites(ctx, group, runs, func(Run) bool { return false })
	out := &Failures{Group: group, Clients: make(map[string]*ClientFailures)}

	for _, run := range runs {
		name, key := firstClient(run)

		client := out.Clients[name]
		if client == nil {
			client = &ClientFailures{
				Version:    cleanVersion(name, run.Versions[key]),
				Commit:     parseCommit(name, run.Versions[key]),
				Simulators: make(map[string]*SimulatorFailures),
			}
			out.Clients[name] = client
		}

		sim := simulatorName(run.Name)
		if _, done := client.Simulators[sim]; done {
			continue
		}

		var meta *RunMetadata
		if suite := suites[run.FileName]; suite != nil {
			meta = suite.RunMetadata
		}

		if client.Repo == "" {
			client.Repo, client.Branch = buildInfo(meta, name)
		}

		client.Simulators[sim] = &SimulatorFailures{
			RunDate:         run.Start,
			SuiteFile:       run.FileName,
			FixturesRelease: fixturesRelease(meta),
			TotalFails:      run.Fails,
			FailRate:        rate(run.Fails, run.NTests),
			TotalTests:      run.NTests,
		}
		client.TotalFails += run.Fails
		out.TotalFails += run.Fails
	}

	total := 0

	for _, client := range out.Clients {
		tests := 0
		for _, sim := range client.Simulators {
			tests += sim.TotalTests
		}

		client.FailRate = rate(client.TotalFails, tests)
		total += tests
	}

	out.FailRate = rate(out.TotalFails, total)

	return out, nil
}

func rate(part, total int) float64 {
	if total == 0 {
		return 0
	}

	return float64(part) / float64(total) * 100
}

// Tests lists the tests of a group's latest runs that match filter, from
// offset, at most limit of them. A limit of 0 means all.
func (s *Store) Tests(ctx context.Context, group string, filter Filter, offset, limit int) (*TestList, error) {
	re, err := filter.regexp()
	if err != nil {
		return nil, err
	}

	l, err := s.listing(ctx, group)
	if err != nil {
		return nil, err
	}

	var runs []Run

	for _, run := range l.latest {
		if (filter.Status == "fail" && run.Fails == 0) || (filter.Status == "pass" && run.Passes == 0) {
			continue
		}

		if filter.matchRun(run) {
			runs = append(runs, run)
		}
	}

	suites := s.readSuites(ctx, group, runs, func(Run) bool { return true })
	out := &TestList{Group: group, Tests: []Test{}}

	for _, run := range runs {
		suite := suites[run.FileName]
		if suite == nil {
			continue
		}

		client, _ := firstClient(run)
		sim := simulatorName(run.Name)

		for _, t := range suite.Tests {
			if !filter.matchTest(t, re) {
				continue
			}

			out.Total++
			if out.Total <= offset || (limit > 0 && len(out.Tests) >= limit) {
				continue
			}

			out.Tests = append(out.Tests, s.entry(group, run, client, sim, t))
		}
	}

	return out, nil
}

// Search lists the tests whose pytest ID contains query, ignoring case.
func (s *Store) Search(ctx context.Context, group, query string, filter Filter, limit int) (*TestList, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	filter.Pattern = regexp.QuoteMeta(query)

	return s.Tests(ctx, group, filter, 0, limit)
}

func (s *Store) entry(group string, run Run, client, sim string, t SuiteTest) Test {
	module, function := splitTestName(t.Name)
	id := strconv.FormatUint(uint64(t.ID), 10)

	return Test{
		Client:    client,
		Simulator: sim,
		Module:    module,
		Function:  function,
		PytestID:  t.Name,
		Fork:      forkOf(t.Name),
		Pass:      t.Pass,
		SuiteFile: run.FileName,
		TestID:    id,
		SourceURL: t.Source,
		HiveURL:   s.TestURL(group, run.FileName, id),
	}
}

// Detail returns one test with its error, logs and reproduce commands. With
// fullLog the whole client log is returned, otherwise its last 30 lines.
func (s *Store) Detail(ctx context.Context, group, fileName, testID string, fullLog bool) (*TestDetail, error) {
	id, err := strconv.ParseUint(testID, 10, 32)
	if err != nil {
		return nil, ErrTestNotFound
	}

	tc, suite, err := s.testCase(ctx, group, fileName, uint32(id))
	if err != nil {
		return nil, err
	}

	name := pytestID(tc.Name)
	module, function := splitTestName(name)
	out := &TestDetail{
		Test: Test{
			Module:    module,
			Function:  function,
			PytestID:  name,
			Fork:      forkOf(name),
			Pass:      tc.SummaryResult.Pass,
			SuiteFile: fileName,
			TestID:    testID,
			SourceURL: sourceURL(tc.Description),
			HiveURL:   s.TestURL(group, fileName, testID),
		},
		FillCommand:    "uv run fill " + name,
		ConsumeCommand: consumeCommand(tc.Description),
		HiveCommand:    hiveCommand(tc.Description),
		Error:          tc.SummaryResult.Details,
	}

	if log := tc.SummaryResult.Log; log != nil && suite.TestDetailsLog != "" {
		if data, err := s.fetchRange(ctx, group, suite.TestDetailsLog, log.Begin, log.End); err == nil {
			out.DetailLog = string(data)
		}
	}

	for _, info := range tc.ClientInfo {
		out.Client = clientName(info.Name)
		if info.LogFile == "" {
			break
		}

		// Fetch only this test's part of the client log when hive recorded
		// where it is.
		begin, end := int64(-1), int64(-1)
		if info.LogOffsets != nil {
			begin, end = info.LogOffsets.Begin, info.LogOffsets.End
		}

		if data, err := s.fetchRange(ctx, group, info.LogFile, begin, end); err == nil {
			out.ClientLog = string(data)
			if !fullLog {
				out.ClientLog = tailLines(out.ClientLog, 30)
			}
		}

		break
	}

	return out, nil
}

// Find returns the detail of the first test matching filter.
func (s *Store) Find(ctx context.Context, group string, filter Filter, fullLog bool) (*TestDetail, error) {
	if filter.Pattern == "" {
		return nil, fmt.Errorf("filter is required")
	}

	found, err := s.Tests(ctx, group, filter, 0, 1)
	if err != nil {
		return nil, err
	}

	if len(found.Tests) == 0 {
		return nil, ErrTestNotFound
	}

	t := found.Tests[0]

	return s.Detail(ctx, group, t.SuiteFile, t.TestID, fullLog)
}

// Diff compares the failures of a group's latest two batches of runs. Without
// tests it returns only the totals, since a group with many failures can
// list tens of thousands of tests.
func (s *Store) Diff(ctx context.Context, group string, withTests bool) (*Diff, error) {
	l, err := s.listing(ctx, group)
	if err != nil {
		return nil, err
	}

	batches := batchRuns(l.runs)
	if len(batches) < 2 {
		return nil, fmt.Errorf("group %q has fewer than two batches of runs to compare", group)
	}

	current := s.batchFailures(ctx, group, batches[0])
	previous := s.batchFailures(ctx, group, batches[1])

	out := &Diff{
		Group:       group,
		FromRun:     batches[1][0].Start,
		ToRun:       batches[0][0].Start,
		Regressions: make(map[string]*ClientDiff),
		Fixes:       make(map[string]*ClientDiff),
		Unchanged:   make(map[string]*ClientDiff),
	}

	add := func(section map[string]*ClientDiff, f failure) {
		client := section[f.client]
		if client == nil {
			client = &ClientDiff{Simulators: make(map[string]*SimulatorDiff)}
			section[f.client] = client
		}

		sim := client.Simulators[f.sim]
		if sim == nil {
			sim = &SimulatorDiff{}
			client.Simulators[f.sim] = sim
		}

		if withTests {
			sim.Tests = append(sim.Tests, s.entry(group, f.run, f.client, f.sim, f.test))
		}

		sim.Total++
		client.Total++
	}

	for key, f := range current {
		if _, before := previous[key]; before {
			add(out.Unchanged, f)
		} else {
			add(out.Regressions, f)
		}
	}

	for key, f := range previous {
		if _, now := current[key]; !now {
			add(out.Fixes, f)
		}
	}

	return out, nil
}

// batchRuns groups runs, newest first, into batches that started within
// batchWindow of the batch's first run.
func batchRuns(runs []Run) [][]Run {
	var batches [][]Run

	for _, run := range runs {
		placed := false

		for i := range batches {
			gap := run.Start.Sub(batches[i][0].Start)
			if gap < 0 {
				gap = -gap
			}

			if gap < batchWindow {
				batches[i] = append(batches[i], run)
				placed = true

				break
			}
		}

		if !placed {
			batches = append(batches, []Run{run})
		}
	}

	return batches
}

type failure struct {
	run    Run
	client string
	sim    string
	test   SuiteTest
}

func (s *Store) batchFailures(ctx context.Context, group string, runs []Run) map[string]failure {
	var failing []Run

	for _, run := range runs {
		if run.Fails > 0 {
			failing = append(failing, run)
		}
	}

	suites := s.readSuites(ctx, group, failing, func(Run) bool { return true })
	out := make(map[string]failure)

	for _, run := range failing {
		suite := suites[run.FileName]
		if suite == nil {
			continue
		}

		client, _ := firstClient(run)
		sim := simulatorName(run.Name)

		for _, t := range suite.Tests {
			if !t.Pass {
				key := strings.Join([]string{client, run.Name, t.Name}, "/")
				out[key] = failure{run: run, client: client, sim: sim, test: t}
			}
		}
	}

	return out
}
