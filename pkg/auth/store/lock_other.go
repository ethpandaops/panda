//go:build !unix || aix

package store

// tryFileLock is a no-op on platforms without flock(2). Process-local
// serialization via refreshMu still applies; cross-process coordination is
// simply unavailable, matching the historical behaviour.
func tryFileLock(_ string) (release func(), acquired bool, err error) {
	return func() {}, true, nil
}
