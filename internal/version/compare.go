package version

import (
	"strconv"
	"strings"
)

// IsNewer returns true if remote is a newer semver than local.
// Handles "dev" and "unknown" gracefully — local "dev" always considers
// remote newer.
func IsNewer(local, remote string) bool {
	local = Clean(local)
	remote = Clean(remote)

	if local == "dev" || local == "unknown" || local == "" {
		return remote != "" && remote != "dev" && remote != "unknown"
	}

	localParts := parseSemver(local)
	remoteParts := parseSemver(remote)

	if localParts == nil || remoteParts == nil {
		return false
	}

	for i := range 3 {
		if remoteParts[i] > localParts[i] {
			return true
		}

		if remoteParts[i] < localParts[i] {
			return false
		}
	}

	return false
}

// Clean strips a leading "v" prefix from a version string.
func Clean(v string) string {
	return strings.TrimPrefix(v, "v")
}

// parseSemver splits a version string into [major, minor, patch].
// Returns nil if the string is not a valid semver.
func parseSemver(v string) []int {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}

	result := make([]int, 3)

	for i, p := range parts {
		// Strip any pre-release suffix (e.g. "1-rc1" -> "1").
		if idx := strings.IndexByte(p, '-'); idx >= 0 {
			p = p[:idx]
		}

		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}

		result[i] = n
	}

	return result
}
