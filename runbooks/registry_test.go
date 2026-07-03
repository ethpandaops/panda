package runbooks

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/types"
)

func TestRunbookRefURIUsesFileStem(t *testing.T) {
	rb := types.Runbook{
		Name:     "Investigate Finality Delay",
		FilePath: "finality_delay.md",
	}

	require.Equal(t, "runbooks://finality_delay", RefURI(rb))
}

func TestRunbookRefURISlugifiesNameFallback(t *testing.T) {
	rb := types.Runbook{Name: "Investigate Finality Delay"}

	require.Equal(t, "runbooks://investigate_finality_delay", RefURI(rb))
}

// runbookRefPattern matches runbook refs wherever they appear. It deliberately
// includes `.` so a ref written flush against a sentence period (outside
// backticks) captures the period and fails resolution — enforcing the
// backticked-ref convention rather than silently ignoring the typo.
var runbookRefPattern = regexp.MustCompile(`runbooks://([a-zA-Z0-9_.-]+)`)

func TestRunbookCrossReferencesResolve(t *testing.T) {
	reg, err := NewRegistry(testLogger())
	require.NoError(t, err)

	for _, rb := range reg.All() {
		t.Run(rb.FilePath, func(t *testing.T) {
			matches := runbookRefPattern.FindAllStringSubmatch(rb.Content, -1)
			for _, match := range matches {
				require.Len(t, match, 2)
				require.NotNilf(t, reg.GetByRef(match[1]), "unresolved runbook ref %q", match[0])
			}
		})
	}
}

// TestRepoRunbookRefsResolve extends ref checking to the repo surfaces outside
// the registry that hardcode runbook names — agent skills, project docs, and
// user-facing CLI help. Same-repo refs are kept hard (deterministic dispatch)
// precisely because this test makes a rename fail here instead of rotting
// silently. pkg/server is excluded: its comments use deliberately typo'd refs
// as resolver examples.
func TestRepoRunbookRefsResolve(t *testing.T) {
	reg, err := NewRegistry(testLogger())
	require.NoError(t, err)

	roots := []string{
		filepath.Join("..", ".claude", "skills"),
		filepath.Join("..", "pkg", "cli"),
	}
	files := []string{
		filepath.Join("..", "AGENTS.md"),
		filepath.Join("..", "CLAUDE.md"),
	}

	for _, root := range roots {
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if d.IsDir() || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}

			return nil
		})
		require.NoError(t, walkErr)
	}

	checked := 0

	for _, path := range files {
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)

		for _, match := range runbookRefPattern.FindAllStringSubmatch(string(data), -1) {
			checked++

			require.NotNilf(t, reg.GetByRef(match[1]),
				"%s: unresolved runbook ref %q (renamed or deleted runbook?)", path, match[0])
		}
	}

	require.Positive(t, checked, "expected runbook refs in skills/CLI — the walk or pattern is broken")
}

func TestRunbookRefsAreUnique(t *testing.T) {
	reg, err := NewRegistry(testLogger())
	require.NoError(t, err)

	seen := make(map[string]string, len(reg.All()))

	for _, rb := range reg.All() {
		ref := RefURI(rb)
		require.NotEmpty(t, ref)

		if previous, ok := seen[ref]; ok {
			t.Fatalf("duplicate ref %s for %s and %s", ref, previous, rb.FilePath)
		}

		seen[ref] = rb.FilePath
	}
}

// TestRunbookRetrievalSurface enforces the retrieval-surface rules from
// AGENTS.md: descriptions fit the embed budget, triggers exist (the loader
// already rejects missing ones), tags exist, and bodies stay under the size cap.
func TestRunbookRetrievalSurface(t *testing.T) {
	reg, err := NewRegistry(testLogger())
	require.NoError(t, err)

	for _, rb := range reg.All() {
		t.Run(rb.FilePath, func(t *testing.T) {
			require.LessOrEqual(t, len(rb.Description), 1024, "description exceeds 1024 chars")
			require.NotEmpty(t, rb.Triggers, "runbook needs example caller queries in triggers")
			require.NotEmpty(t, rb.Tags, "runbook needs tags")

			lines := strings.Count(rb.Content, "\n") + 1
			require.LessOrEqualf(t, lines, 500, "body has %d lines; split or trim it", lines)
		})
	}
}

func TestRunbooksAreDiscoverableByTags(t *testing.T) {
	reg, err := NewRegistry(testLogger())
	require.NoError(t, err)

	tags := reg.Tags()
	for _, tag := range []string{"devnet", "kurtosis", "panda-compute", "clickhouse", "ethereum"} {
		require.Truef(t, slices.Contains(tags, tag), "expected tag %q in %s", tag, strings.Join(tags, ", "))
	}
}

func testLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return log
}
