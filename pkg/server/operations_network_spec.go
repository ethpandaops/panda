package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/operations"
)

const (
	// networkSpecBaseURL is the ethpandaops devnet spec namespace on
	// notes.ethereum.org. The network id is appended to form the page URL.
	networkSpecBaseURL = "https://notes.ethereum.org/@ethpandaops/"

	networkSpecHTTPTimeout = 15 * time.Second

	// networkSpecMaxBytes caps the markdown download size.
	networkSpecMaxBytes = 2 << 20 // 2 MiB
)

// networkSpecAllowedHosts restricts spec fetches to the notes.ethereum.org /
// HackMD surfaces, both as an SSRF guard on caller-supplied urls and to keep
// the operation focused on devnet spec documents.
var networkSpecAllowedHosts = map[string]bool{
	"notes.ethereum.org": true,
	"hackmd.io":          true,
}

// networkSpecSection is a level-2 (##) section of a spec page. Content is the
// verbatim markdown under the heading — callers target a section by name rather
// than relying on deeper parsing.
type networkSpecSection struct {
	Heading string `json:"heading"`
	Content string `json:"content"`
}

// networkSpecResponse is the data payload for network.spec. The raw markdown is
// the source of truth; sections are a light index into it by heading.
type networkSpecResponse struct {
	Network  string               `json:"network"`
	URL      string               `json:"url"`
	Title    string               `json:"title,omitempty"`
	Sections []networkSpecSection `json:"sections,omitempty"`
	Markdown string               `json:"markdown"`
}

func (s *service) handleNetworkSpec(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := requiredStringArg(req.Args, "network")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	pageURL, err := resolveNetworkSpecURL(id, optionalStringArg(req.Args, "url"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	markdown, status, err := s.fetchNetworkSpec(r.Context(), pageURL)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: parseNetworkSpec(id, pageURL, markdown),
	})
}

// resolveNetworkSpecURL builds the spec page URL for a network, honoring an
// optional caller override. The host must be on the allowlist.
func resolveNetworkSpecURL(id, override string) (string, error) {
	raw := networkSpecBaseURL + id
	if override != "" {
		raw = override
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid spec url: %w", err)
	}

	if parsed.Scheme != "https" || !networkSpecAllowedHosts[parsed.Host] {
		return "", fmt.Errorf("spec url host must be one of notes.ethereum.org, hackmd.io")
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}

// fetchNetworkSpec downloads the raw markdown for a spec page via HackMD's
// /download endpoint.
func (s *service) fetchNetworkSpec(ctx context.Context, pageURL string) (string, int, error) {
	downloadURL := pageURL
	if !strings.HasSuffix(downloadURL, "/download") {
		downloadURL += "/download"
	}

	requestCtx, cancel := context.WithTimeout(ctx, networkSpecHTTPTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("creating spec request: %w", err)
	}

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("fetching spec: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, networkSpecMaxBytes+1))
	if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("reading spec: %w", err)
	}

	if int64(len(body)) > networkSpecMaxBytes {
		return "", http.StatusBadGateway, fmt.Errorf("spec exceeded %d bytes", networkSpecMaxBytes)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", http.StatusNotFound, fmt.Errorf("no spec page found at %s", pageURL)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("spec fetch failed (HTTP %d)", resp.StatusCode)
	}

	return string(body), http.StatusOK, nil
}

// parseNetworkSpec indexes a spec page: title and level-2 sections, with the raw
// markdown carried through untouched.
func parseNetworkSpec(id, pageURL, markdown string) networkSpecResponse {
	lines := strings.Split(markdown, "\n")

	return networkSpecResponse{
		Network:  id,
		URL:      pageURL,
		Title:    parseSpecTitle(lines),
		Sections: parseSpecSections(lines),
		Markdown: markdown,
	}
}

func parseSpecTitle(lines []string) string {
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}

	return ""
}

// parseSpecSections splits the document into ordered level-2 (##) sections,
// keeping each section's content as verbatim markdown.
func parseSpecSections(lines []string) []networkSpecSection {
	var (
		sections []networkSpecSection
		heading  string
		content  []string
	)

	flush := func() {
		if heading == "" {
			return
		}

		sections = append(sections, networkSpecSection{
			Heading: heading,
			Content: strings.TrimSpace(strings.Join(content, "\n")),
		})
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ") {
			flush()

			heading = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			content = content[:0]

			continue
		}

		content = append(content, line)
	}

	flush()

	return sections
}
