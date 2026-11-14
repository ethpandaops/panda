package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

// rolloorURLPattern is where a network's rolloor answers. Devnets adopt
// rolloor one at a time, so a network runs it exactly when this answers.
var rolloorURLPattern = "https://rolloor.%s.ethpandaops.io"

// rolloorProbeTTL is how long a probe result is trusted.
const rolloorProbeTTL = 5 * time.Minute

// rolloorNetworkPattern guards the network segment used to build URLs.
var rolloorNetworkPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// rolloorActions are the verbs the rolloor API takes under /actions.
var rolloorActions = map[string]bool{
	"sync": true, "refresh": true, "pause": true, "promote": true, "abort": true,
	"retry": true, "suspend": true, "resume": true,
}

// rolloorDirectory remembers which networks answered as running rolloor.
type rolloorDirectory struct {
	mu      sync.Mutex
	results map[string]rolloorProbe
}

type rolloorProbe struct {
	ok bool
	at time.Time
}

func (s *service) rolloorDirectory() *rolloorDirectory {
	s.rolloorOnce.Do(func() {
		s.rolloorDir = &rolloorDirectory{results: map[string]rolloorProbe{}}
	})

	return s.rolloorDir
}

func (s *service) handleRolloorOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "rolloor.list_networks":
		s.handleRolloorListNetworks(w, r)
	case "rolloor.fleet":
		s.handleRolloorGet(w, r, func(map[string]any) (string, error) { return "/api/v1/fleet", nil })
	case "rolloor.rollouts":
		s.handleRolloorGet(w, r, func(map[string]any) (string, error) { return "/api/v1/rollouts", nil })
	case "rolloor.rollout":
		s.handleRolloorGet(w, r, func(args map[string]any) (string, error) {
			id, err := requiredStringArg(args, "id")
			if err != nil {
				return "", err
			}

			return "/api/v1/rollouts/" + url.PathEscape(id), nil
		})
	case "rolloor.history":
		s.handleRolloorGet(w, r, func(args map[string]any) (string, error) {
			q := url.Values{}
			q.Set("limit", fmt.Sprint(optionalIntArg(args, "limit", 50)))

			if g := optionalStringArg(args, "group"); g != "" {
				q.Set("group", g)
			}

			if sel := optionalStringArg(args, "selector"); sel != "" {
				q.Set("selector", sel)
			}

			return "/api/v1/history?" + q.Encode(), nil
		})
	case "rolloor.action":
		s.handleRolloorAction(w, r)
	default:
		return false
	}

	return true
}

// rolloorCandidates are the networks worth probing: the active devnets.
func (s *service) rolloorCandidates() []string {
	if s.cartographoorClient == nil {
		return nil
	}

	var names []string

	for name := range s.cartographoorClient.GetActiveNetworks() {
		if strings.Contains(name, "devnet") && rolloorNetworkPattern.MatchString(name) {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	return names
}

// rolloorNetworks probes every candidate not probed recently and returns
// the networks running rolloor, with their base URLs.
func (s *service) rolloorNetworks(ctx context.Context) map[string]string {
	dir := s.rolloorDirectory()
	candidates := s.rolloorCandidates()
	now := time.Now()

	var stale []string

	dir.mu.Lock()

	for _, name := range candidates {
		if p, ok := dir.results[name]; !ok || now.Sub(p.at) > rolloorProbeTTL {
			stale = append(stale, name)
		}
	}

	dir.mu.Unlock()

	var wg sync.WaitGroup

	for _, name := range stale {
		wg.Add(1)

		go func() {
			defer wg.Done()

			ok := s.rolloorAnswers(ctx, name)

			dir.mu.Lock()
			dir.results[name] = rolloorProbe{ok: ok, at: now}
			dir.mu.Unlock()
		}()
	}

	wg.Wait()

	out := map[string]string{}

	dir.mu.Lock()
	defer dir.mu.Unlock()

	for _, name := range candidates {
		if dir.results[name].ok {
			out[name] = fmt.Sprintf(rolloorURLPattern, name)
		}
	}

	return out
}

// rolloorAnswers reports whether a network's rolloor health endpoint answers
// as rolloor.
func (s *service) rolloorAnswers(ctx context.Context, network string) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(rolloorURLPattern, network)+"/healthz", nil)
	if err != nil {
		return false
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	var health struct {
		OK          bool   `json:"ok"`
		Environment string `json:"environment"`
	}

	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&health) != nil {
		return false
	}

	return health.OK && health.Environment != ""
}

// rolloorBaseURL resolves a network argument to its rolloor, or says which
// networks have one.
func (s *service) rolloorBaseURL(ctx context.Context, args map[string]any) (string, string, int, error) {
	network, err := requiredStringArg(args, "network")
	if err != nil {
		return "", "", http.StatusBadRequest, err
	}

	if !rolloorNetworkPattern.MatchString(network) {
		return "", "", http.StatusBadRequest, fmt.Errorf("invalid network name %q", network)
	}

	networks := s.rolloorNetworks(ctx)
	if base, ok := networks[network]; ok {
		return network, base, http.StatusOK, nil
	}

	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}

	sort.Strings(names)

	if len(names) == 0 {
		return "", "", http.StatusNotFound, fmt.Errorf("rolloor is not running on %s; no network runs it yet", network)
	}

	return "", "", http.StatusNotFound, fmt.Errorf("rolloor is not running on %s. Networks with rolloor: %s", network, strings.Join(names, ", "))
}

func (s *service) handleRolloorListNetworks(w http.ResponseWriter, r *http.Request) {
	networks := s.rolloorNetworks(r.Context())

	items := make([]listItem, 0, len(networks))
	for name, base := range networks {
		items = append(items, listItem{Name: name, URL: base, Type: "rolloor"})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"networks": items},
	})
}

// handleRolloorGet reads from a network's rolloor directly: its reads are
// public, like Dora's.
func (s *service) handleRolloorGet(w http.ResponseWriter, r *http.Request, path func(map[string]any) (string, error)) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	network, base, status, err := s.rolloorBaseURL(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())

		return
	}

	p, err := path(req.Args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p, nil)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())

		return
	}

	hreq.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(hreq)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "reaching rolloor: "+err.Error())

		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "reading rolloor: "+err.Error())

		return
	}

	s.writeRolloorResult(w, resp.StatusCode, body, network, base)
}

// handleRolloorAction sends one verb to a network's rolloor through the
// proxy, which forwards the caller's own token; rolloor decides whether this
// person may act.
func (s *service) handleRolloorAction(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil || !s.credentials.Status().Authenticated {
		writeAPIError(w, http.StatusUnauthorized, "panda auth required to act on a rollout: run 'panda auth login' first")

		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	verb, err := requiredStringArg(req.Args, "verb")
	if err != nil || !rolloorActions[verb] {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("verb must be one of sync, refresh, pause, promote, abort, retry, suspend, resume (got %q)", verb))

		return
	}

	network, base, status, err := s.rolloorBaseURL(r.Context(), req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())

		return
	}

	payload, err := json.Marshal(optionalMapArg(req.Args, "body"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	body, status, _, err := s.proxyRequest(r.Context(), http.MethodPost, "/rolloor/"+network+"/api/v1/actions/"+verb,
		bytes.NewReader(payload), http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		writeAPIError(w, status, err.Error())

		return
	}

	s.writeRolloorResult(w, status, body, network, base)
}

// writeRolloorResult hands rolloor's JSON back as the operation's data, or
// its error with rolloor's own reason.
func (s *service) writeRolloorResult(w http.ResponseWriter, status int, body []byte, network, base string) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("rolloor answered %d with something that is not JSON", status))

		return
	}

	if status < 200 || status >= 300 {
		msg := fmt.Sprintf("rolloor answered %d", status)
		if obj, ok := data.(map[string]any); ok {
			if e, ok := obj["error"].(string); ok && e != "" {
				msg = e
			}
		}

		writeAPIError(w, status, msg)

		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: data,
		Meta: map[string]any{"network": network, "url": base},
	})
}
