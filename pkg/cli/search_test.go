package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

func TestSearchQueryFromArgsOrFlag(t *testing.T) {
	assert.Equal(t, "validator duty latency", queryFromArgsOrFlag([]string{"validator", "duty", "latency"}, ""))
	assert.Equal(t, "flag query", queryFromArgsOrFlag([]string{"positional"}, "flag query"))
}

func TestSearchQueryArgsOrFlagRequiresQuery(t *testing.T) {
	query := ""
	err := queryArgsOrFlag(&query)(testCommand(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a query")

	query = "from flag"
	require.NoError(t, queryArgsOrFlag(&query)(testCommand(), nil))
}

func TestPrintRunbookSummariesShowsRef(t *testing.T) {
	output := captureStdout(t, func() {
		printRunbookSummaries([]*serverapi.SearchRunbookResult{{
			Name:            "Debug",
			Description:     "Investigate a network issue.",
			Tags:            []string{"debugging"},
			Prerequisites:   []string{"clickhouse"},
			Ref:             "runbooks://Debug",
			SimilarityScore: 0.7,
		}})
	})

	assert.Contains(t, output, "Debug")
	assert.Contains(t, output, "Read full content: panda read runbooks://Debug")
}

func TestCompactRunbookResponsePreservesRef(t *testing.T) {
	resp := &serverapi.SearchRunbooksResponse{
		Results: []*serverapi.SearchRunbookResult{{
			Name: "Debug",
			Ref:  "runbooks://Debug",
		}},
	}

	compact := compactRunbookResponse(resp)
	require.Len(t, compact.Results, 1)
	assert.Equal(t, "runbooks://Debug", compact.Results[0].Ref)
}
