package hive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is where ethPandaOps CI publishes hive results.
const DefaultBaseURL = "https://hive.ethpandaops.io"

const (
	// listingTTL is how long a group list or listing is served before it
	// is fetched again. CI publishes new runs every few hours.
	listingTTL = 5 * time.Minute
	// maxParallel caps how many suite files are fetched at once.
	maxParallel = 16
	// maxBody caps any one response, so a bad upstream cannot exhaust memory.
	maxBody = 100 << 20
)

var (
	// ErrUnknownGroup is returned for a group discovery.json does not list.
	ErrUnknownGroup = errors.New("unknown hive group")
	// ErrTestNotFound is returned for a test number a suite does not have.
	ErrTestNotFound = errors.New("test case not found")
)

// Store fetches hive results on demand and caches what queries need.
type Store struct {
	baseURL string
	client  *http.Client

	mu       sync.Mutex
	groups   map[string]string // group -> its address, ending in /
	order    []string
	groupsAt time.Time
	listings map[string]*listing

	suitesMu sync.RWMutex
	suites   map[string]*Suite // "group/fileName" -> suite

	names *interner
}

type listing struct {
	runs   []Run // newest first
	latest []Run // the latest run per client and simulator
	at     time.Time
}

// NewStore returns a store reading from baseURL with client.
func NewStore(baseURL string, client *http.Client) *Store {
	return &Store{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   client,
		listings: make(map[string]*listing),
		suites:   make(map[string]*Suite),
		names:    newInterner(),
	}
}

// Groups returns the result groups, in discovery.json order.
func (s *Store) Groups(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.discoverLocked(ctx); err != nil {
		return nil, err
	}

	return append([]string(nil), s.order...), nil
}

func (s *Store) discoverLocked(ctx context.Context) error {
	if s.groups != nil && time.Since(s.groupsAt) < listingTTL {
		return nil
	}

	body, err := s.get(ctx, s.baseURL+"/discovery.json", "")
	if err != nil {
		return err
	}

	var entries []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return fmt.Errorf("decode discovery.json: %w", err)
	}

	groups := make(map[string]string, len(entries))
	order := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}

		// A group's results can live under another path, mainnet's under
		// /generic/.
		address := entry.Address
		if address == "" {
			address = s.baseURL + "/" + entry.Name + "/"
		}

		groups[entry.Name] = strings.TrimRight(address, "/") + "/"
		order = append(order, entry.Name)
	}

	s.groups, s.order, s.groupsAt = groups, order, time.Now()

	return nil
}

// listing returns a group's runs, fetching them when stale.
func (s *Store) listing(ctx context.Context, group string) (*listing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.discoverLocked(ctx); err != nil {
		return nil, err
	}

	address, ok := s.groups[group]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownGroup, group)
	}

	if cached := s.listings[group]; cached != nil && time.Since(cached.at) < listingTTL {
		return cached, nil
	}

	body, err := s.get(ctx, address+"listing.jsonl", "")
	if err != nil {
		return nil, err
	}

	var runs []Run

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var run Run
		if err := json.Unmarshal(line, &run); err != nil {
			continue // a malformed line should not hide the others
		}

		runs = append(runs, run)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s listing: %w", group, err)
	}

	sort.Slice(runs, func(i, j int) bool { return runs[i].Start.After(runs[j].Start) })

	fresh := &listing{runs: runs, latest: latestRuns(runs), at: time.Now()}
	s.listings[group] = fresh
	s.evict(group, runs)

	return fresh, nil
}

// evict drops cached suites of a group's runs that left its listing.
func (s *Store) evict(group string, runs []Run) {
	keep := make(map[string]bool, len(runs))
	for _, run := range runs {
		keep[group+"/"+run.FileName] = true
	}

	s.suitesMu.Lock()
	defer s.suitesMu.Unlock()

	evicted := false

	for key := range s.suites {
		if strings.HasPrefix(key, group+"/") && !keep[key] {
			delete(s.suites, key)

			evicted = true
		}
	}

	// Start a new intern table, so names of evicted suites do not stay
	// alive through it.
	if evicted {
		s.names.reset()
	}
}

// latestRuns returns the newest run per simulator and client. runs must be
// sorted newest first.
func latestRuns(runs []Run) []Run {
	seen := make(map[string]bool)

	var latest []Run

	for _, run := range runs {
		fresh := false

		for _, client := range run.Clients {
			if !seen[run.Name+"/"+client] {
				fresh = true
			}
		}

		if fresh {
			for _, client := range run.Clients {
				seen[run.Name+"/"+client] = true
			}

			latest = append(latest, run)
		}
	}

	return latest
}

// suite returns a cached suite, reading it when needed. Without tests it
// reads only the run metadata at the top of the file.
func (s *Store) suite(ctx context.Context, group, fileName string, withTests bool) (*Suite, error) {
	key := group + "/" + fileName

	s.suitesMu.RLock()
	cached, ok := s.suites[key]
	s.suitesMu.RUnlock()

	if ok && (cached.HasTests || !withTests) {
		return cached, nil
	}

	address, err := s.address(ctx, group)
	if err != nil {
		return nil, err
	}

	url := address + "results/" + fileName

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	read, err := readSuite(io.LimitReader(resp.Body, maxBody), withTests, s.names.get)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}

	s.suitesMu.Lock()
	defer s.suitesMu.Unlock()

	// Keep a copy with tests that a concurrent read already stored.
	if old, ok := s.suites[key]; ok && old.HasTests && !read.HasTests {
		return old, nil
	}

	s.suites[key] = read

	return read, nil
}

// readSuites reads many suites in parallel, ignoring those that fail, so one
// bad file does not hide a group's other results.
func (s *Store) readSuites(ctx context.Context, group string, runs []Run, withTests func(Run) bool) map[string]*Suite {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make(map[string]*Suite, len(runs))
		sem = make(chan struct{}, maxParallel)
	)

	for _, run := range runs {
		wg.Add(1)

		go func(run Run) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			read, err := s.suite(ctx, group, run.FileName, withTests(run))
			if err != nil {
				return
			}

			mu.Lock()
			out[run.FileName] = read
			mu.Unlock()
		}(run)
	}

	wg.Wait()

	return out
}

func (s *Store) address(ctx context.Context, group string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.discoverLocked(ctx); err != nil {
		return "", err
	}

	address, ok := s.groups[group]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownGroup, group)
	}

	return address, nil
}

// readSuite reads a suite file. hive writes the run metadata before the
// tests, so without tests it stops there and the rest is never read.
func readSuite(r io.Reader, withTests bool, intern func(string) string) (*Suite, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}

	suite := &Suite{}

	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}

		switch key {
		case "runMetadata":
			if err := dec.Decode(&suite.RunMetadata); err != nil {
				return nil, err
			}

			if !withTests {
				return suite, nil
			}
		case "testDetailsLog":
			if err := dec.Decode(&suite.TestDetailsLog); err != nil {
				return nil, err
			}
		case "testCases":
			if err := readTests(dec, suite, intern); err != nil {
				return nil, err
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
		}
	}

	return suite, nil
}

// readTests reads the testCases object, keeping per test only its name,
// result, source link and where it is in the file.
func readTests(dec *json.Decoder, suite *Suite, intern func(string) string) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}

	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}

		name, _ := key.(string)

		id, err := strconv.ParseUint(name, 10, 32)
		if err != nil {
			return fmt.Errorf("test id %q: %w", name, err)
		}

		start := dec.InputOffset()

		var tc struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			SummaryResult struct {
				Pass bool `json:"pass"`
			} `json:"summaryResult"`
		}
		if err := dec.Decode(&tc); err != nil {
			return err
		}

		suite.Tests = append(suite.Tests, SuiteTest{
			Name:   intern(pytestID(tc.Name)),
			Source: intern(sourceURL(tc.Description)),
			Offset: start,
			Size:   int32(dec.InputOffset() - start),
			ID:     uint32(id),
			Pass:   tc.SummaryResult.Pass,
		})
	}

	if err := expectDelim(dec, '}'); err != nil {
		return err
	}

	sort.Slice(suite.Tests, func(i, j int) bool { return suite.Tests[i].ID < suite.Tests[j].ID })
	suite.HasTests = true

	return nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}

	if tok != want {
		return fmt.Errorf("expected %v, got %v", want, tok)
	}

	return nil
}

// testCase reads one test case in full by fetching only its bytes of the
// suite file.
func (s *Store) testCase(ctx context.Context, group, fileName string, id uint32) (*TestCase, *Suite, error) {
	suite, err := s.suite(ctx, group, fileName, true)
	if err != nil {
		return nil, nil, err
	}

	i := sort.Search(len(suite.Tests), func(i int) bool { return suite.Tests[i].ID >= id })
	if i == len(suite.Tests) || suite.Tests[i].ID != id {
		return nil, suite, ErrTestNotFound
	}

	t := suite.Tests[i]

	data, err := s.fetchRange(ctx, group, fileName, t.Offset, t.Offset+int64(t.Size))
	if err != nil {
		return nil, suite, err
	}

	// The range starts after the test's key, at the ':' before its value.
	var tc TestCase
	if err := json.Unmarshal(bytes.TrimLeft(data, " \t\r\n:"), &tc); err != nil {
		return nil, suite, fmt.Errorf("decode test %d of %s: %w", id, fileName, err)
	}

	return &tc, suite, nil
}

// fetchRange returns the bytes in [begin, end) of a results file, or all of
// it when the range is negative.
func (s *Store) fetchRange(ctx context.Context, group, path string, begin, end int64) ([]byte, error) {
	address, err := s.address(ctx, group)
	if err != nil {
		return nil, err
	}

	byteRange := ""
	if begin >= 0 && end > begin {
		byteRange = fmt.Sprintf("bytes=%d-%d", begin, end-1)
	}

	data, err := s.get(ctx, address+"results/"+path, byteRange)
	if err != nil {
		return nil, err
	}

	// A server that ignores Range sends the whole file.
	if byteRange != "" && int64(len(data)) > end-begin && end <= int64(len(data)) {
		return data[begin:end], nil
	}

	return data, nil
}

func (s *Store) get(ctx context.Context, url, byteRange string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// TestURL links to a test's page in the hive UI, which shows its
// description, error and client logs.
func (s *Store) TestURL(group, fileName, testID string) string {
	return fmt.Sprintf("%s/#/test/%s/%s?testnumber=%s",
		s.baseURL, group, strings.TrimSuffix(fileName, ".json"), testID)
}

// interner returns one shared copy of each string it is given.
type interner struct {
	mu sync.Mutex
	m  map[string]string
}

func newInterner() *interner {
	return &interner{m: make(map[string]string)}
}

func (in *interner) get(s string) string {
	in.mu.Lock()
	defer in.mu.Unlock()

	if shared, ok := in.m[s]; ok {
		return shared
	}

	// Copy it, so a substring does not keep the whole string it was cut
	// from alive.
	s = strings.Clone(s)
	in.m[s] = s

	return s
}

func (in *interner) reset() {
	in.mu.Lock()
	defer in.mu.Unlock()

	in.m = make(map[string]string)
}
