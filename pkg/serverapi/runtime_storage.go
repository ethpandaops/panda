package serverapi

import (
	"fmt"
	"strings"
)

// RuntimeStorageScopedPrefix returns the storage prefix scoped to an execution.
func RuntimeStorageScopedPrefix(executionID, prefix string) string {
	relativePrefix := runtimeStorageRelativeKey(executionID, prefix)
	if relativePrefix == "" {
		return runtimeStorageExecutionPrefix(executionID)
	}

	return runtimeStorageExecutionPrefix(executionID) + relativePrefix
}

// RuntimeStorageScopedKey returns the full scoped key and the relative key for a given execution.
func RuntimeStorageScopedKey(executionID, key string) (string, string, error) {
	relativeKey := runtimeStorageRelativeKey(executionID, key)
	if relativeKey == "" {
		return "", "", fmt.Errorf("key is required")
	}

	return runtimeStorageExecutionPrefix(executionID) + relativeKey, relativeKey, nil
}

func runtimeStorageExecutionPrefix(executionID string) string {
	return strings.TrimLeft(strings.TrimSpace(executionID), "/") + "/"
}

func runtimeStorageRelativeKey(executionID, key string) string {
	trimmed := strings.TrimLeft(strings.TrimSpace(key), "/")
	if trimmed == "" {
		return ""
	}

	return strings.TrimPrefix(trimmed, runtimeStorageExecutionPrefix(executionID))
}
