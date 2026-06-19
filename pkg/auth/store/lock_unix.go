//go:build unix && !aix

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// tryFileLock attempts a non-blocking exclusive lock on path+".lock" so that
// only one process drives a credential refresh at a time when several panda
// processes share one credentials file.
//
// It returns (release, true, nil) when this process holds the lock,
// (nil, false, nil) when another process currently holds it, and
// (no-op release, true, err) when the lock file itself cannot be created — in
// which case the caller proceeds relying on process-local serialization, so a
// single process is never blocked by lock-file errors.
//
// The lock is advisory and released automatically by the OS when the holding
// process exits, so a crashed holder never leaves a stale lock.
func tryFileLock(path string) (release func(), acquired bool, err error) {
	lockPath := path + ".lock"

	if mkErr := os.MkdirAll(filepath.Dir(lockPath), 0o700); mkErr != nil {
		return func() {}, true, fmt.Errorf("creating lock directory: %w", mkErr)
	}

	f, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		return func() {}, true, fmt.Errorf("opening lock file: %w", openErr)
	}

	if flockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); flockErr != nil {
		// EWOULDBLOCK/EAGAIN is genuine contention: another process holds the
		// lock, so back off and reuse what it writes. Any other flock error is
		// unexpected — fail open and refresh without cross-process coordination
		// rather than risk being unable to refresh at all.
		if errors.Is(flockErr, unix.EWOULDBLOCK) || errors.Is(flockErr, unix.EAGAIN) {
			_ = f.Close()

			return nil, false, nil
		}

		return func() { _ = f.Close() }, true, fmt.Errorf("locking credentials file: %w", flockErr)
	}

	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
