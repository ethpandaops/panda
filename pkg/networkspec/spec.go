package networkspec

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// BaseURL is the ethpandaops devnet spec namespace on
	// notes.ethereum.org. The network id is appended to form the page URL.
	BaseURL = "https://notes.ethereum.org/@ethpandaops/"

	HTTPTimeout = 15 * time.Second

	// MaxBytes caps the markdown download size.
	MaxBytes = 2 << 20 // 2 MiB
)

// AllowedHosts restricts spec fetches to the notes.ethereum.org /
// HackMD surfaces, both as an SSRF guard on caller-supplied urls and to keep
// the operation focused on devnet spec documents.
var AllowedHosts = map[string]bool{
	"notes.ethereum.org": true,
	"hackmd.io":          true,
}

// Section is a level-2 (##) section of a spec page. Content is the
// verbatim markdown under the heading.
type Section struct {
	Heading string `json:"heading"`
	Content string `json:"content,omitempty"`
}

type EIP struct {
	ID         string   `json:"id"`
	Title      string   `json:"title,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	SpecRef    string   `json:"spec_ref,omitempty"`
	SpecURL    string   `json:"spec_url,omitempty"`
	URL        string   `json:"url,omitempty"`
	StatusText string   `json:"status_text,omitempty"`
}

type SystemContract struct {
	Contract    string `json:"contract"`
	Requirement string `json:"requirement,omitempty"`
	Type        string `json:"type,omitempty"`
	Address     string `json:"address"`
}

type Release struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
	Status  string `json:"status,omitempty"`
}

type Image struct {
	Layer   string `json:"layer,omitempty"`
	Client  string `json:"client,omitempty"`
	Image   string `json:"image"`
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
}

type localTesting struct {
	NetworkParams      map[string]any `json:"network_params,omitempty"`
	AdditionalServices []string       `json:"additional_services,omitempty"`
	ParticipantImages  []Image        `json:"participant_images,omitempty"`
	ConfigYAML         string         `json:"config_yaml,omitempty"`
}

// Response is the compact, LLM-oriented spec digest embedded in
// networks://{id} resources.
type Response struct {
	Network           string           `json:"network"`
	URL               string           `json:"url"`
	Title             string           `json:"title,omitempty"`
	Notices           []string         `json:"notices,omitempty"`
	EIPs              []EIP            `json:"eips,omitempty"`
	SystemContracts   []SystemContract `json:"system_contracts,omitempty"`
	Releases          []Release        `json:"releases,omitempty"`
	ParticipantImages []Image          `json:"participant_images,omitempty"`
	MetricsURL        string           `json:"metrics_url,omitempty"`
	PreviousSpecURL   string           `json:"previous_spec_url,omitempty"`
}

var (
	eipIDPattern        = regexp.MustCompile(`EIP-\d+`)
	markdownLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	specRefPattern      = regexp.MustCompile("`?spec@([^`\\s\\]]+)`?")
	urlPattern          = regexp.MustCompile(`https://[^\s)]+`)
)

// ResolveURL builds the spec page URL for a network, honoring an
// optional caller override. The host must be on the allowlist.
func ResolveURL(id, override string) (string, error) {
	raw := BaseURL + id
	if override != "" {
		raw = override
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid spec url: %w", err)
	}

	if parsed.Scheme != "https" || !AllowedHosts[parsed.Host] {
		return "", fmt.Errorf("spec url host must be one of notes.ethereum.org, hackmd.io")
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}

// Fetch downloads the raw markdown for a spec page via HackMD's
// /download endpoint.
func Fetch(ctx context.Context, client *http.Client, pageURL string) (string, int, error) {
	downloadURL := pageURL
	if !strings.HasSuffix(downloadURL, "/download") {
		downloadURL += "/download"
	}

	requestCtx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("creating spec request: %w", err)
	}

	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("fetching spec: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("reading spec: %w", err)
	}

	if int64(len(body)) > MaxBytes {
		return "", http.StatusBadGateway, fmt.Errorf("spec exceeded %d bytes", MaxBytes)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", http.StatusNotFound, fmt.Errorf("no spec page found at %s", pageURL)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("spec fetch failed (HTTP %d)", resp.StatusCode)
	}

	return string(body), http.StatusOK, nil
}

// FetchAndParse downloads the conventional or overridden spec page and returns
// the compact digest. Missing pages are returned as HTTP 404 errors so callers
// can decide whether that is fatal for their surface.
func FetchAndParse(ctx context.Context, client *http.Client, id, override string) (*Response, int, error) {
	pageURL, err := ResolveURL(id, override)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	markdown, status, err := Fetch(ctx, client, pageURL)
	if err != nil {
		return nil, status, err
	}

	response := Parse(id, pageURL, markdown)

	return &response, http.StatusOK, nil
}

// Parse digests a spec page into a compact, structured shape for
// LLM callers.
func Parse(id, pageURL, markdown string) Response {
	lines := strings.Split(markdown, "\n")
	sections := parseSpecSections(lines)
	sectionIndex := specSectionsByHeading(sections)
	eipSection := sectionIndex["eip list"]

	response := Response{
		Network:           id,
		URL:               pageURL,
		Title:             parseSpecTitle(lines),
		Notices:           parseSpecNotices(lines),
		EIPs:              parseSpecEIPs(markdownBeforeSubheading(eipSection)),
		SystemContracts:   parseSpecSystemContracts(lines),
		Releases:          parseSpecReleases(markdownSubsection(eipSection, "Test Releases")),
		ParticipantImages: parseSpecParticipantImages(sectionIndex["local testing"]),
		MetricsURL:        parseSpecMetricsURL(sectionIndex["metrics"]),
		PreviousSpecURL:   parseSpecPreviousSpecURL(lines, pageURL),
	}

	return response
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
func parseSpecSections(lines []string) []Section {
	var (
		sections []Section
		heading  string
		content  []string
	)

	flush := func() {
		if heading == "" {
			return
		}

		sections = append(sections, Section{
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

func specSectionsByHeading(sections []Section) map[string]string {
	index := make(map[string]string, len(sections))
	for _, section := range sections {
		key := normalizeSpecHeading(section.Heading)
		if key != "" {
			index[key] = section.Content
		}
	}

	return index
}

func normalizeSpecHeading(heading string) string {
	lower := strings.ToLower(strings.TrimSpace(heading))
	for _, suffix := range []string{" for "} {
		if before, _, ok := strings.Cut(lower, suffix); ok && strings.TrimSpace(before) == "eip list" {
			return "eip list"
		}
	}

	return lower
}

func markdownBeforeSubheading(markdown string) string {
	lines := strings.Split(markdown, "\n")
	var out []string

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "### ") {
			break
		}

		out = append(out, line)
	}

	return strings.TrimSpace(strings.Join(out, "\n"))
}

func markdownSubsection(markdown, heading string) string {
	lines := strings.Split(markdown, "\n")
	target := strings.ToLower(strings.TrimSpace(heading))
	inSection := false
	var out []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			current := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "### ")))
			if inSection && current != target {
				break
			}

			inSection = current == target
			continue
		}

		if inSection {
			out = append(out, line)
		}
	}

	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseSpecNotices(lines []string) []string {
	var notices []string

	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), ":::info") {
			continue
		}

		var block []string
		for i++; i < len(lines); i++ {
			line := strings.TrimSpace(lines[i])
			if strings.HasPrefix(line, ":::") {
				break
			}
			if line != "" && !strings.HasPrefix(line, "|") {
				block = append(block, cleanMarkdownInline(line))
			}
		}

		if text := strings.TrimSpace(strings.Join(block, " ")); text != "" {
			notices = append(notices, text)
		}
	}

	return notices
}

func parseSpecEIPs(section string) []EIP {
	var eips []EIP

	for _, row := range parseMarkdownTableRows(section) {
		if len(row) < 2 {
			continue
		}

		id := eipIDPattern.FindString(row[0])
		if id == "" {
			continue
		}

		statusText := ""
		if len(row) >= 3 {
			statusText = strings.TrimSpace(row[2])
		}
		urls := markdownURLs(row[0])
		eipURL := firstString(urls)
		if eipURL == "" {
			eipURL = canonicalEIPURL(id)
		}

		eips = append(eips, EIP{
			ID:         id,
			Title:      cleanMarkdownInline(row[1]),
			Flags:      specStatusFlags(statusText),
			SpecRef:    firstRegexpSubmatch(specRefPattern, row[0]),
			SpecURL:    secondString(urls),
			URL:        eipURL,
			StatusText: cleanStatusText(statusText),
		})
	}

	return eips
}

func parseSpecSystemContracts(lines []string) []SystemContract {
	rows := parseMarkdownTableRows(strings.Join(lines, "\n"))
	var contracts []SystemContract

	for _, row := range rows {
		if len(row) < 4 || !strings.Contains(row[3], "0x") {
			continue
		}

		contracts = append(contracts, SystemContract{
			Contract:    cleanMarkdownInline(row[0]),
			Requirement: cleanMarkdownInline(row[1]),
			Type:        cleanMarkdownInline(row[2]),
			Address:     cleanMarkdownInline(row[3]),
		})
	}

	return contracts
}

func parseSpecReleases(section string) []Release {
	var releases []Release

	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "**") {
			continue
		}

		nameEnd := strings.Index(line[2:], ":**")
		if nameEnd < 0 {
			continue
		}
		name := strings.TrimSpace(line[2 : 2+nameEnd])
		rest := strings.TrimSpace(line[2+nameEnd+3:])

		version := cleanMarkdownInline(rest)
		link := markdownLinkPattern.FindStringSubmatch(rest)
		url := ""
		if len(link) == 3 {
			version = strings.TrimSpace(link[1])
			url = strings.TrimSpace(link[2])
		}
		if url == "" {
			url = specReleaseURL(name, version)
		}

		releases = append(releases, Release{
			Name:    name,
			Version: version,
			URL:     url,
			Status:  specStatus(rest),
		})
	}

	return releases
}

func canonicalEIPURL(id string) string {
	if id == "" {
		return ""
	}

	return "https://eips.ethereum.org/EIPS/" + strings.ToLower(id)
}

func specReleaseURL(name, version string) string {
	version = firstString(strings.Fields(version))
	if version == "" || !strings.HasPrefix(version, "v") {
		return ""
	}

	var repo string
	lowerName := strings.ToLower(name)
	switch {
	case strings.Contains(lowerName, "consensus"):
		repo = "ethereum/consensus-specs"
	case strings.Contains(lowerName, "execution spec tests") || strings.Contains(lowerName, "eest"):
		repo = "ethereum/execution-spec-tests"
	case strings.Contains(lowerName, "execution"):
		repo = "ethereum/execution-specs"
	case strings.Contains(lowerName, "builder"):
		repo = "ethereum/builder-specs"
	default:
		return ""
	}

	return "https://github.com/" + repo + "/releases/tag/" + url.PathEscape(version)
}

func parseSpecLocalTesting(section string) *localTesting {
	code, ok := firstFencedCodeBlock(section, "yaml")
	if !ok {
		return nil
	}

	var config map[string]any
	if err := yaml.Unmarshal([]byte(code), &config); err != nil {
		return &localTesting{ConfigYAML: strings.TrimSpace(code)}
	}

	return &localTesting{
		NetworkParams:      mapValue(config, "network_params"),
		AdditionalServices: stringSliceValue(config, "additional_services"),
		ParticipantImages:  participantImages(config),
	}
}

func parseSpecParticipantImages(section string) []Image {
	local := parseSpecLocalTesting(section)
	if local == nil {
		return nil
	}

	return local.ParticipantImages
}

func parseSpecMetricsURL(section string) string {
	return firstURL(section)
}

func parseSpecPreviousSpecURL(lines []string, currentURL string) string {
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "previous") || !strings.Contains(lower, "spec") {
			continue
		}

		for _, candidate := range urlPattern.FindAllString(line, -1) {
			candidate = strings.TrimRight(candidate, ".,")
			if isNetworkSpecNotesURL(candidate, currentURL) {
				return candidate
			}
		}
	}

	return ""
}

func isNetworkSpecNotesURL(candidate, currentURL string) bool {
	return candidate != currentURL &&
		strings.Contains(candidate, "notes.ethereum.org/@ethpandaops/") &&
		!strings.Contains(candidate, "/bal-otel")
}

func parseMarkdownTableRows(markdown string) [][]string {
	var rows [][]string

	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}

		cells := splitMarkdownTableRow(trimmed)
		if len(cells) == 0 || isMarkdownTableSeparator(cells) || isMarkdownTableHeader(cells) {
			continue
		}

		rows = append(rows, cells)
	}

	return rows
}

func splitMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")

	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}

	return cells
}

func isMarkdownTableSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}

	for _, cell := range cells {
		cell = strings.Trim(cell, "-: ")
		if cell != "" {
			return false
		}
	}

	return true
}

func isMarkdownTableHeader(cells []string) bool {
	if len(cells) == 0 {
		return false
	}

	first := strings.ToLower(strings.TrimSpace(cells[0]))
	return first == "eip" || first == "contract"
}

func specStatusFlags(status string) []string {
	var flags []string
	lower := strings.ToLower(status)

	for _, item := range []struct {
		token string
		flag  string
	}{
		{":new:", "new"},
		{" new", "new"},
		{":up:", "updated"},
		{"updated", "updated"},
		{":exclamation:", "attention"},
		{"optional", "optional"},
	} {
		if strings.Contains(lower, item.token) {
			flags = append(flags, item.flag)
		}
	}

	return flags
}

func specStatus(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "open") || strings.Contains(lower, ":exclamation:"):
		return "open"
	case strings.Contains(lower, "draft"):
		return "draft"
	case strings.Contains(lower, "merged") || strings.Contains(lower, ":heavy_check_mark:"):
		return "merged"
	default:
		return ""
	}
}

func cleanStatusText(status string) string {
	replacements := map[string]string{
		":new:":              "new",
		":up:":               "updated",
		":exclamation:":      "attention",
		":heavy_check_mark:": "merged",
	}

	for from, to := range replacements {
		status = strings.ReplaceAll(status, from, to)
	}

	return strings.TrimSpace(status)
}

func cleanMarkdownInline(text string) string {
	text = markdownLinkPattern.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "`", "")
	text = cleanStatusText(text)
	text = strings.Join(strings.Fields(text), " ")

	return strings.TrimSpace(text)
}

func markdownURLs(text string) []string {
	matches := markdownLinkPattern.FindAllStringSubmatch(text, -1)
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 {
			urls = append(urls, strings.TrimSpace(match[2]))
		}
	}

	return urls
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

func secondString(values []string) string {
	if len(values) < 2 {
		return ""
	}

	return values[1]
}

func firstRegexpSubmatch(re *regexp.Regexp, text string) string {
	match := re.FindStringSubmatch(text)
	if len(match) >= 2 {
		return strings.TrimSpace(match[1])
	}

	return ""
}

func firstURL(text string) string {
	match := urlPattern.FindString(text)
	return strings.TrimRight(match, ".,")
}

func firstFencedCodeBlock(markdown, language string) (string, bool) {
	lines := strings.Split(markdown, "\n")
	inBlock := false
	var block []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "```") {
				info := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				if strings.HasPrefix(strings.ToLower(info), language) {
					inBlock = true
					block = block[:0]
				}
			}

			continue
		}

		if strings.HasPrefix(trimmed, "```") {
			return strings.TrimSpace(strings.Join(block, "\n")), true
		}

		block = append(block, line)
	}

	return "", false
}

func mapValue(parent map[string]any, key string) map[string]any {
	value, _ := parent[key].(map[string]any)
	return value
}

func stringSliceValue(parent map[string]any, key string) []string {
	raw, ok := parent[key].([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}

	return out
}

func participantImages(config map[string]any) []Image {
	matrix, ok := config["participants_matrix"].(map[string]any)
	if !ok {
		return nil
	}

	var images []Image
	for _, layer := range []string{"el", "cl"} {
		participants, ok := matrix[layer].([]any)
		if !ok {
			continue
		}

		for _, raw := range participants {
			participant, ok := raw.(map[string]any)
			if !ok {
				continue
			}

			client, _ := participant[layer+"_type"].(string)
			image, _ := participant[layer+"_image"].(string)
			if image == "" {
				continue
			}

			images = append(images, Image{
				Layer:   layer,
				Client:  client,
				Image:   image,
				Version: imageTag(image),
				URL:     dockerImageURL(image),
			})
		}
	}

	return images
}

func imageTag(image string) string {
	idx := strings.LastIndex(image, ":")
	if idx < 0 || idx == len(image)-1 {
		return ""
	}

	return image[idx+1:]
}

func dockerImageURL(image string) string {
	ref := strings.TrimSpace(image)
	if ref == "" {
		return ""
	}

	if before, _, ok := strings.Cut(ref, "@"); ok {
		ref = before
	}

	tag := imageTag(ref)
	if tag != "" {
		ref = strings.TrimSuffix(ref, ":"+tag)
	}

	parts := strings.Split(ref, "/")
	if len(parts) < 2 {
		return ""
	}

	first := parts[0]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return ""
	}

	link := "https://hub.docker.com/r/" + ref
	if tag != "" {
		link += "/tags?name=" + url.QueryEscape(tag)
	}

	return link
}
