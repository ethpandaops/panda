package hive

import (
	"regexp"
	"strings"
)

// commitPatterns extract the commit hash from each client's version string.
var commitPatterns = map[string]*regexp.Regexp{
	"go-ethereum": regexp.MustCompile(`Geth/v[\d.]+-\w+-([0-9a-f]{8,})`),
	"reth":        regexp.MustCompile(`(?i)reth.*\+([0-9a-f]{7,})`),
	"besu":        regexp.MustCompile(`besu/v[\w.]+-\w+-([0-9a-f]{7,})`),
	"nethermind":  regexp.MustCompile(`[\d.]+(?:-\w+)?\+([0-9a-f]{7,})`),
	"erigon":      regexp.MustCompile(`[\d.]+-\w+-([0-9a-f]{7,})`),
	"nimbus-el":   regexp.MustCompile(`Nimbus/v[\d.]+-([0-9a-f]{7,})`),
	"ethrex":      regexp.MustCompile(`ethrex/v[\w.-]+-([0-9a-f]{7,})`),
}

var anyCommit = regexp.MustCompile(`[^0-9a-f]([0-9a-f]{7,40})[^0-9a-f]`)

// versionPatterns extract a short version from each client's version string.
var versionPatterns = map[string]*regexp.Regexp{
	"go-ethereum": regexp.MustCompile(`Geth/(v[\d.]+-\w+-[0-9a-f]+)`),
	"reth":        regexp.MustCompile(`(?i)reth\s*(?:version:?\s*)?(\d[\w.+]+)`),
	"besu":        regexp.MustCompile(`besu/(v[\w.]+-\w+-[0-9a-f]+)`),
	"nethermind":  regexp.MustCompile(`^([\d]+\.[\d]+\.[\w.+-]+)`),
	"erigon":      regexp.MustCompile(`^([\d]+\.[\d]+\.[\w.+-]+)`),
	"nimbus-el":   regexp.MustCompile(`Nimbus/(v[\d.]+-[0-9a-f]+)`),
	"ethrex":      regexp.MustCompile(`ethrex/(v[\w.-]+)`),
}

var (
	hiveCommandPattern    = regexp.MustCompile(`\./hive --sim[^<]+`)
	consumeCommandPattern = regexp.MustCompile(`uv run consume[^<]+`)
	fixturesPattern       = regexp.MustCompile(`fixtures=(https://[^\s"<]+)`)
)

// clientName strips the nametag from a hive client key, reth_default is reth.
func clientName(key string) string {
	for _, name := range []string{"go-ethereum", "nimbus-el"} {
		if strings.HasPrefix(key, name) {
			return name
		}
	}

	if i := strings.LastIndex(key, "_"); i > 0 {
		return key[:i]
	}

	return key
}

// firstClient returns the name and key of a run's first client.
func firstClient(run Run) (name, key string) {
	if len(run.Clients) == 0 {
		return "", ""
	}

	return clientName(run.Clients[0]), run.Clients[0]
}

// simulatorName returns a suite's short simulator name, as consume-engine
// for eels/consume-engine.
func simulatorName(suite string) string {
	if i := strings.LastIndex(suite, "/"); i >= 0 {
		return suite[i+1:]
	}

	return suite
}

func parseCommit(client, version string) string {
	if pattern, ok := commitPatterns[client]; ok {
		if m := pattern.FindStringSubmatch(version); len(m) > 1 {
			return m[1]
		}
	}

	if m := anyCommit.FindStringSubmatch(" " + version + " "); len(m) > 1 {
		return m[1]
	}

	return ""
}

func cleanVersion(client, raw string) string {
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i]
	}

	raw = strings.TrimSpace(raw)

	pattern, ok := versionPatterns[client]
	if !ok {
		return raw
	}

	m := pattern.FindStringSubmatch(raw)
	if len(m) < 2 {
		return raw
	}

	version := m[1]
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	// ethrex puts a full commit hash at the end, keep its first 8.
	if client == "ethrex" {
		parts := strings.Split(version, "-")
		if last := parts[len(parts)-1]; len(parts) > 1 && len(last) > 12 && isHex(last) {
			parts[len(parts)-1] = last[:8]
			version = strings.Join(parts, "-")
		}
	}

	return version
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// splitTestName splits module::function[params] into module and function.
func splitTestName(name string) (module, function string) {
	module, function, found := strings.Cut(name, "::")
	if !found {
		return name, ""
	}

	if i := strings.IndexByte(function, '['); i >= 0 {
		function = function[:i]
	}

	return module, function
}

// pytestID strips the client suffix from a hive test name:
// module::function[params]-reth_default is module::function[params].
func pytestID(name string) string {
	if i := strings.LastIndex(name, "]"); i >= 0 {
		return name[:i+1]
	}

	return name
}

// forkOf returns the fork in a test's parameters, [fork_Amsterdam-...].
func forkOf(name string) string {
	i := strings.Index(name, "[fork_")
	if i < 0 {
		return ""
	}

	rest := name[i+len("[fork_"):]
	if end := strings.IndexAny(rest, "-]"); end >= 0 {
		return rest[:end]
	}

	return ""
}

// sourceURL returns the [source] link in a test's description. It runs on
// every test as suites load, so it searches strings instead of a regexp.
func sourceURL(description string) string {
	end := strings.Index(description, `">[source]`)
	if end < 0 {
		return ""
	}

	start := strings.LastIndex(description[:end], `href="`)
	if start < 0 {
		return ""
	}

	url := description[start+len(`href="`) : end]
	if !strings.HasPrefix(url, "https://github.com/") || strings.Contains(url, `"`) {
		return ""
	}

	return url
}

func hiveCommand(description string) string {
	return cleanHTML(hiveCommandPattern.FindString(description))
}

func consumeCommand(description string) string {
	return cleanHTML(consumeCommandPattern.FindString(description))
}

func cleanHTML(s string) string {
	s = strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		`\<br/>`, "", "<br/>", "",
	).Replace(s)

	return strings.TrimSpace(s)
}

// fixturesRelease returns the fixtures URL a run was started with.
func fixturesRelease(meta *RunMetadata) string {
	if meta == nil {
		return ""
	}

	for i, arg := range meta.HiveCommand {
		if arg == "--sim.buildarg" && i+1 < len(meta.HiveCommand) {
			if m := fixturesPattern.FindStringSubmatch(meta.HiveCommand[i+1]); len(m) > 1 {
				return m[1]
			}
		}
	}

	return ""
}

// buildInfo returns the repo and branch a run built a client from.
func buildInfo(meta *RunMetadata, client string) (repo, branch string) {
	if meta == nil || meta.ClientConfig == nil || meta.ClientConfig.Content == nil {
		return "", ""
	}

	for _, entry := range meta.ClientConfig.Content.Clients {
		if entry.Client == client {
			return entry.BuildArgs["github"], entry.BuildArgs["tag"]
		}
	}

	return "", ""
}

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}

	return strings.Join(lines[len(lines)-n:], "\n")
}
