package version

import (
	"strconv"
	"strings"
)

// IsNewer returns true if remote is a newer semver than local.
// Pre-release versions order per semver: 1.2.0-rc.1 < 1.2.0-rc.2 < 1.2.0.
// Handles "dev" and "unknown" gracefully — local "dev" always considers
// remote newer.
func IsNewer(local, remote string) bool {
	local = Clean(local)
	remote = Clean(remote)

	if local == "dev" || local == "unknown" || local == "" {
		return remote != "" && remote != "dev" && remote != "unknown"
	}

	localCore, localPre, ok := parseSemver(local)
	if !ok {
		return false
	}

	remoteCore, remotePre, ok := parseSemver(remote)
	if !ok {
		return false
	}

	for i := range 3 {
		if remoteCore[i] > localCore[i] {
			return true
		}

		if remoteCore[i] < localCore[i] {
			return false
		}
	}

	return comparePrerelease(remotePre, localPre) > 0
}

// Clean strips a leading "v" prefix from a version string.
func Clean(v string) string {
	return strings.TrimPrefix(v, "v")
}

// comparePrerelease orders two pre-release suffixes per semver §11.
// An empty suffix (a full release) ranks above any pre-release.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}

	aIDs := strings.Split(a, ".")
	bIDs := strings.Split(b, ".")

	for i := 0; i < len(aIDs) && i < len(bIDs); i++ {
		if c := compareIdentifier(aIDs[i], bIDs[i]); c != 0 {
			return c
		}
	}

	// All shared identifiers equal: the longer set ranks higher (rc.1.1 > rc.1).
	switch {
	case len(aIDs) > len(bIDs):
		return 1
	case len(aIDs) < len(bIDs):
		return -1
	}

	return 0
}

// compareIdentifier orders one dot-separated pre-release identifier:
// numeric identifiers compare numerically and rank below alphanumeric
// ones; alphanumeric identifiers compare lexically (semver §11.4).
func compareIdentifier(a, b string) int {
	aNum, aErr := strconv.Atoi(a)
	bNum, bErr := strconv.Atoi(b)

	switch {
	case aErr == nil && bErr == nil:
		switch {
		case aNum > bNum:
			return 1
		case aNum < bNum:
			return -1
		}

		return 0
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// parseSemver splits a version into its numeric core and pre-release
// suffix. Build metadata ("+...") is ignored. ok is false when the core
// is not three dot-separated numbers.
func parseSemver(v string) (core [3]int, prerelease string, ok bool) {
	if idx := strings.IndexByte(v, '+'); idx >= 0 {
		v = v[:idx]
	}

	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		prerelease = v[idx+1:]
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return core, "", false
	}

	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return core, "", false
		}

		core[i] = n
	}

	return core, prerelease, true
}
