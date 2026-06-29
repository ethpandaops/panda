package runbooks

import (
	"testing"

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
