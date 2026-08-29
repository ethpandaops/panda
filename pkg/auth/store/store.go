// Package store provides local credential storage for OAuth tokens.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth/client"
)

const (
	// credentialLockWait bounds how long an interactive write (login/logout)
	// waits for an in-flight refresh to release the credentials file lock. A
	// refresh holds the lock across its token-endpoint request, which the auth
	// client caps at 30s, so this must exceed that to ride through a slow-but-
	// valid refresh and only surface ErrCredentialBusy when one is truly stuck.
	credentialLockWait = 35 * time.Second

	// credentialLockPoll is the retry interval while waiting for the lock.
	credentialLockPoll = 50 * time.Millisecond
)

// ErrCredentialDowngrade is returned by Save when the new tokens cannot refresh
// (no refresh token) but the stored credential can. Replacing a refreshable
// credential with one that cannot refresh would silently downgrade a working
// session to one that dies at the next access-token expiry.
var ErrCredentialDowngrade = errors.New("refusing to overwrite a refreshable credential with one that cannot refresh")

// ErrCredentialBusy is returned by Save/Clear when another process holds the
// credentials file lock (a refresh is in progress) for longer than the write
// lock wait. The caller should retry shortly rather than clobber the rotation.
var ErrCredentialBusy = errors.New("credentials are being refreshed by another process; retry shortly")

// ErrReauthRequired is returned once the provider has rejected the stored
// refresh token as invalid_grant. With a rotating provider this is terminal:
// no retry with the same token can succeed, so the store stops calling the
// token endpoint until a new credential is written (e.g. `panda auth login`).
var ErrReauthRequired = errors.New("re-authentication required: run 'panda auth login'")

// Store manages local credential storage.
type Store interface {
	// Path returns the resolved credentials file path.
	Path() string

	// Save saves tokens to the store.
	Save(tokens *client.Tokens) error

	// Load loads tokens from the store.
	Load() (*client.Tokens, error)

	// Clear removes stored tokens.
	Clear() error

	// GetAccessToken returns a valid access token, refreshing if needed.
	GetAccessToken() (string, error)

	// Invalidate forces the next GetAccessToken to refresh the access token,
	// even if it has not yet hit the local expiry buffer. Used when the proxy
	// rejects a token that should still be valid locally (e.g. revocation).
	Invalidate()

	// IsAuthenticated returns true if valid tokens are stored.
	IsAuthenticated() bool
}

// Config configures the credential store.
type Config struct {
	// Path is the path to the credentials file.
	// Defaults to a namespaced file in ~/.config/panda/credentials/
	Path string

	// IssuerURL namespaces stored credentials by auth issuer.
	IssuerURL string

	// ClientID namespaces stored credentials by OAuth client.
	ClientID string

	// Resource namespaces stored credentials by requested resource.
	Resource string

	// RefreshBuffer is how long before expiry to refresh the token.
	RefreshBuffer time.Duration

	// RefreshTokenTTL is the expected lifetime of the refresh token.
	// When set, the store will trigger a refresh at 50% of this duration
	// to keep the refresh token alive via provider rotation.
	RefreshTokenTTL time.Duration

	// WriteLockWait bounds how long Save/Clear wait for the credentials file
	// lock before returning ErrCredentialBusy. Defaults to credentialLockWait.
	WriteLockWait time.Duration

	// AuthClient is the OAuth client for refreshing tokens.
	AuthClient client.Client
}

// store implements the Store interface.
type store struct {
	log          logrus.FieldLogger
	cfg          Config
	mu           sync.RWMutex
	tokens       *client.Tokens
	forceRefresh bool
	refreshMu    sync.Mutex

	// deadRefreshToken is the refresh token the provider last rejected with
	// invalid_grant. While the on-disk credential still carries this token,
	// refresh attempts fail fast with ErrReauthRequired instead of calling the
	// token endpoint again; a credential written with any other refresh token
	// clears it.
	deadRefreshToken string
}

// New creates a new credential store.
func New(log logrus.FieldLogger, cfg Config) Store {
	if cfg.Path == "" {
		cfg.Path = defaultCredentialPath(cfg)
	}

	if cfg.RefreshBuffer == 0 {
		cfg.RefreshBuffer = 5 * time.Minute
	}

	if cfg.WriteLockWait == 0 {
		cfg.WriteLockWait = credentialLockWait
	}

	return &store{
		log: log.WithField("component", "credential-store"),
		cfg: cfg,
	}
}

func (s *store) Path() string {
	return s.cfg.Path
}

// Save persists tokens for login/logout-style writes. It serializes with
// concurrent refreshers in other processes via the credentials file lock and
// refuses to replace a refreshable credential with one that cannot refresh
// (ErrCredentialDowngrade), so an outdated login cannot silently downgrade a
// working session.
func (s *store) Save(tokens *client.Tokens) error {
	release, ok := s.lockForWrite()
	if !ok {
		return ErrCredentialBusy
	}
	defer release()

	if tokens.RefreshToken == "" {
		// Fail closed: if we cannot read the existing credential we must not
		// assume it is safe to replace with a non-refreshable one.
		existing, err := s.Load()
		if err != nil {
			return fmt.Errorf("checking existing credential before overwrite: %w", err)
		}

		if existing != nil && existing.RefreshToken != "" {
			return ErrCredentialDowngrade
		}
	}

	return s.writeTokens(tokens)
}

// lockForWrite acquires the credentials file lock for an interactive write,
// waiting up to WriteLockWait for an in-flight refresh to finish. Persistent
// contention fails closed (ok=false) so the caller surfaces a retry rather than
// clobbering a rotation. A lock-infrastructure error (not contention) proceeds
// best-effort, since the atomic write still prevents corruption.
func (s *store) lockForWrite() (release func(), ok bool) {
	deadline := time.Now().Add(s.cfg.WriteLockWait)

	for {
		releaseLock, acquired, err := tryFileLock(s.cfg.Path)
		if err != nil {
			s.log.WithError(err).Warn(
				"Credential file lock unavailable; writing without cross-process coordination",
			)

			return releaseLock, true
		}

		if acquired {
			return releaseLock, true
		}

		if time.Now().After(deadline) {
			return nil, false
		}

		time.Sleep(credentialLockPoll)
	}
}

// writeTokens atomically persists tokens and updates the in-memory cache. It
// does not take the file lock: callers needing cross-process exclusion (Save,
// Clear) acquire it first, and refresh already holds it.
func (s *store) writeTokens(tokens *client.Tokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure directory exists.
	dir := filepath.Dir(s.cfg.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Marshal tokens.
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling tokens: %w", err)
	}

	// Write atomically via a temp file + rename so a crash mid-write never
	// leaves partial JSON or loses the only valid rotated refresh token.
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp credentials file: %w", err)
	}

	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing temp credentials file: %w", err)
	}

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("setting credentials permissions: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("syncing temp credentials file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp credentials file: %w", err)
	}

	if err := os.Rename(tmpName, s.cfg.Path); err != nil {
		return fmt.Errorf("replacing credentials file: %w", err)
	}

	s.tokens = tokens
	s.deadRefreshToken = ""
	s.log.Debug("Saved credentials")

	return nil
}

// Load loads tokens from the store.
func (s *store) Load() (*client.Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if file exists.
	if _, err := os.Stat(s.cfg.Path); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(s.cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("reading credentials file: %w", err)
	}

	var tokens client.Tokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("unmarshaling tokens: %w", err)
	}

	s.tokens = &tokens

	return &tokens, nil
}

// Clear removes stored tokens, serializing with concurrent refreshers via the
// credentials file lock.
func (s *store) Clear() error {
	release, ok := s.lockForWrite()
	if !ok {
		return ErrCredentialBusy
	}
	defer release()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.cfg.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing credentials file: %w", err)
	}

	s.tokens = nil
	s.deadRefreshToken = ""
	s.log.Debug("Cleared credentials")

	return nil
}

// GetAccessToken returns a valid access token, refreshing if needed.
func (s *store) GetAccessToken() (string, error) {
	tokens, err := s.getTokens()
	if err != nil {
		return "", fmt.Errorf("loading tokens: %w", err)
	}
	if tokens == nil {
		return "", fmt.Errorf("not authenticated")
	}

	// Check if token needs refresh.
	if s.needsRefresh(tokens) {
		if tokens.RefreshToken == "" {
			if time.Now().Before(tokens.ExpiresAt) {
				return tokens.AccessToken, nil
			}

			return "", fmt.Errorf("access token expired and no refresh token available")
		}

		newTokens, err := s.refresh(tokens)
		if err != nil {
			if time.Now().Before(tokens.ExpiresAt) {
				return tokens.AccessToken, nil
			}

			return "", fmt.Errorf("refreshing token: %w", err)
		}
		tokens = newTokens
	}

	return tokens.AccessToken, nil
}

// Invalidate forces the next GetAccessToken to refresh.
func (s *store) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.forceRefresh = true
}

// forceRefreshRequested reports whether Invalidate has armed a forced refresh.
func (s *store) forceRefreshRequested() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.forceRefresh
}

// deadRefreshTokenValue returns the refresh token last rejected as
// invalid_grant, or "" when none is recorded.
func (s *store) deadRefreshTokenValue() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.deadRefreshToken
}

// markDeadRefreshToken records a refresh token the provider rejected as
// invalid_grant so later refresh attempts fail fast until a new credential
// is written.
func (s *store) markDeadRefreshToken(refreshToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deadRefreshToken = refreshToken
}

// clearDeadRefreshToken forgets a recorded invalid_grant rejection.
func (s *store) clearDeadRefreshToken() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deadRefreshToken = ""
}

// clearForceRefresh clears the forced-refresh flag after a successful refresh.
func (s *store) clearForceRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.forceRefresh = false
}

// IsAuthenticated returns true if valid tokens are stored.
func (s *store) IsAuthenticated() bool {
	tokens, err := s.getTokens()
	if err != nil {
		return false
	}
	if tokens == nil {
		return false
	}

	// Check if token is expired.
	return time.Now().Before(tokens.ExpiresAt)
}

// getTokens returns cached tokens or loads them from disk.
func (s *store) getTokens() (*client.Tokens, error) {
	if tokens := s.snapshotTokens(); tokens != nil {
		return tokens, nil
	}

	return s.Load()
}

// snapshotTokens returns the currently cached tokens under a read lock.
func (s *store) snapshotTokens() *client.Tokens {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.tokens
}

// needsRefresh returns true if the token should be refreshed.
func (s *store) needsRefresh(tokens *client.Tokens) bool {
	if tokens == nil {
		return true
	}

	if s.forceRefreshRequested() {
		return true
	}

	// Refresh proactively once the access token passes the configured fraction
	// of its lifetime, or once it is within the buffer of expiry.
	if client.ShouldRefresh(
		time.Now(), tokens.ExpiresAt, tokens.ExpiresIn,
		s.cfg.RefreshBuffer, client.DefaultRefreshFraction,
	) {
		return true
	}

	// Refresh if the refresh token has passed 50% of its expected lifetime.
	// This keeps the refresh token alive via provider rotation so the
	// server doesn't get hard logged out.
	if s.cfg.RefreshTokenTTL > 0 && !tokens.RefreshTokenIssuedAt.IsZero() {
		halfLife := tokens.RefreshTokenIssuedAt.Add(s.cfg.RefreshTokenTTL / 2)
		if time.Now().After(halfLife) {
			return true
		}
	}

	return false
}

// refresh refreshes the access token using the supplied prior tokens.
func (s *store) refresh(prior *client.Tokens) (*client.Tokens, error) {
	if s.cfg.AuthClient == nil {
		return nil, fmt.Errorf("no auth client configured for refresh")
	}

	if prior == nil || prior.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	// Serialize refreshes within this process so concurrent callers do not
	// double-call the provider.
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	// Another in-process caller may have refreshed while we waited for the lock.
	if current := s.snapshotTokens(); current != nil && !s.needsRefresh(current) {
		return current, nil
	}

	// Coordinate across processes that share this credentials file (e.g. several
	// panda servers on one host pointed at the same proxy). Only the process
	// that wins the file lock drives the rotation; the others reuse the token it
	// writes. Without this, each process refreshes the same refresh token and
	// provider-side rotation turns every loser's token into invalid_grant.
	release, acquired, lockErr := tryFileLock(s.cfg.Path)
	if lockErr != nil {
		s.log.WithError(lockErr).Warn(
			"Credential file lock unavailable; refreshing without cross-process coordination",
		)
	}

	if !acquired {
		// Another process is refreshing right now. Do not touch the refresh
		// token — reuse whatever it writes. Refresh is proactive (well before
		// expiry), so a still-valid token is normally available and the winner's
		// rotation lands on the next reload. Only hand back a token that is not
		// yet expired; otherwise surface an error so the caller retries once the
		// holder has written the rotated token.
		s.log.Debug("Another process holds the credential lock; reusing its refreshed token")

		if reloaded, loadErr := s.Load(); loadErr == nil && reloaded != nil &&
			time.Now().Before(reloaded.ExpiresAt) {
			return reloaded, nil
		}

		if time.Now().Before(prior.ExpiresAt) {
			return prior, nil
		}

		return nil, fmt.Errorf("another process is refreshing credentials and the current token has expired")
	}
	defer release()

	// We hold the lock. Another process may have rotated the token just before
	// we acquired it; reload from disk and skip the refresh if it is now fresh.
	if reloaded, loadErr := s.Load(); loadErr == nil && reloaded != nil && reloaded.RefreshToken != "" {
		if !s.needsRefresh(reloaded) {
			s.log.Debug("Credentials already refreshed by another process; reusing")

			return reloaded, nil
		}

		prior = reloaded
	}

	// A refresh token the provider already rejected as invalid_grant can never
	// succeed again under rotation; fail fast without another token-endpoint
	// call. Any different token on disk means a new credential landed (fresh
	// login, or another process won the rotation), which supersedes the
	// rejection.
	if dead := s.deadRefreshTokenValue(); dead != "" {
		if prior.RefreshToken == dead {
			s.log.Debug("Refresh token was already rejected as invalid_grant; waiting for re-authentication")

			return nil, ErrReauthRequired
		}

		s.clearDeadRefreshToken()
	}

	priorIssuedAt := prior.RefreshTokenIssuedAt

	s.log.WithField("expires_at", prior.ExpiresAt.Format(time.RFC3339)).Debug("Refreshing access token")

	newTokens, err := s.cfg.AuthClient.Refresh(context.Background(), prior.RefreshToken)
	if err != nil {
		if isInvalidGrant(err) {
			s.markDeadRefreshToken(prior.RefreshToken)
			s.log.WithError(err).Warn(
				"Refresh token rejected (invalid_grant); it was rotated by another " +
					"refresher sharing these credentials, or revoked — re-authentication is " +
					"required (panda auth login)",
			)

			return nil, fmt.Errorf("%w: %v", ErrReauthRequired, err)
		}

		s.log.WithError(err).Warn("Failed to refresh access token")

		return nil, fmt.Errorf("refreshing token: %w", err)
	}

	// The provider rotates the refresh token when it returns a fresh one (the
	// client stamps a non-zero issued-at). When it does not rotate, preserve the
	// original issued-at so the half-life check stays correct.
	rotated := !newTokens.RefreshTokenIssuedAt.IsZero()
	if newTokens.RefreshTokenIssuedAt.IsZero() {
		newTokens.RefreshTokenIssuedAt = priorIssuedAt
	}

	// We already hold the file lock; write directly without re-acquiring it.
	if err := s.writeTokens(newTokens); err != nil {
		return nil, err
	}

	s.clearForceRefresh()

	s.log.WithFields(logrus.Fields{
		"rotated_refresh_token": rotated,
		"expires_at":            newTokens.ExpiresAt.Format(time.RFC3339),
	}).Info("Refreshed access token")

	return newTokens, nil
}

// isInvalidGrant reports whether a refresh error is an OAuth invalid_grant
// response, which for a rotating provider means the presented refresh token was
// already rotated away or revoked.
func isInvalidGrant(err error) bool {
	return err != nil && strings.Contains(err.Error(), "invalid_grant")
}

func defaultCredentialPath(cfg Config) string {
	home, _ := os.UserHomeDir()
	newBaseDir := filepath.Join(home, ".config", "panda")
	oldBaseDir := filepath.Join(home, ".config", "ethpandaops-mcp")

	key := credentialNamespaceKey(cfg.IssuerURL, cfg.ClientID, cfg.Resource)

	var newPath, oldPath string
	if key == "" {
		newPath = filepath.Join(newBaseDir, "credentials.json")
		oldPath = filepath.Join(oldBaseDir, "credentials.json")
	} else {
		newPath = filepath.Join(newBaseDir, "credentials", key+".json")
		oldPath = filepath.Join(oldBaseDir, "credentials", key+".json")
	}

	// Migrate: if old credentials exist but new ones don't, fall back to old path.
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		if _, err := os.Stat(oldPath); err == nil {
			logrus.Warnf(
				"Using legacy credential path %s; move it to %s",
				oldPath, newPath,
			)

			return oldPath
		}
	}

	return newPath
}

func credentialNamespaceKey(issuerURL, clientID, resource string) string {
	normalized := strings.Join([]string{
		strings.TrimSpace(strings.TrimRight(issuerURL, "/")),
		strings.TrimSpace(clientID),
		strings.TrimSpace(strings.TrimRight(resource, "/")),
	}, "\n")

	if strings.TrimSpace(strings.ReplaceAll(normalized, "\n", "")) == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:8])
}
