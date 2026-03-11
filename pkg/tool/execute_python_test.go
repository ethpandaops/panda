package tool

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/sandbox"
)

func TestFormatExecutionResultIncludesSessionState(t *testing.T) {
	t.Parallel()

	result := &sandbox.ExecutionResult{
		Stdout:              "hello",
		ExitCode:            0,
		DurationSeconds:     1.25,
		SessionID:           "session-1",
		SessionTTLRemaining: 91*time.Second + 400*time.Millisecond,
		SessionFiles: []sandbox.SessionFile{
			{Name: "notes.txt", Size: 1536},
			{Name: "script.py", Size: 12},
		},
	}

	formatted := formatExecutionResult(result, &config.Config{})

	assert.True(t, strings.Contains(formatted, "[stdout]\nhello"))
	assert.True(t, strings.Contains(formatted, "[session] id=session-1 ttl=1m31s"))
	assert.True(t, strings.Contains(formatted, "notes.txt(1.5 KB)"))
	assert.True(t, strings.Contains(formatted, "script.py(12 B)"))
	assert.True(t, strings.Contains(formatted, "[exit=0 duration=1.25s]"))
}

func TestResourceTipCacheMarksOnceAndExpiresByClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 11, 16, 45, 0, 0, time.UTC)
	cache := &resourceTipCache{
		now:     func() time.Time { return now },
		entries: make(map[string]time.Time),
	}

	assert.True(t, cache.markShown("session-1"))
	assert.False(t, cache.markShown("session-1"))

	now = now.Add(resourceTipCacheMaxAge + time.Minute)
	cache.cleanupLocked()
	assert.True(t, cache.markShown("session-2"))
	assert.NotContains(t, cache.entries, "session-1")
}
