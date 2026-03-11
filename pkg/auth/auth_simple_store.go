package auth

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// pendingAuth stores state during the OAuth flow.
type pendingAuth struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	State         string
	CreatedAt     time.Time
}

// issuedCode is an issued authorization code.
type issuedCode struct {
	Code          string
	ClientID      string
	RedirectURI   string
	Resource      string
	CodeChallenge string
	GitHubLogin   string
	GitHubID      int64
	Orgs          []string
	CreatedAt     time.Time
	Used          bool
}

// cleanupLoop periodically removes expired pending auths and codes.
func (s *simpleService) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

func (s *simpleService) cleanup() {
	now := time.Now()

	s.pendingMu.Lock()
	for key, pending := range s.pending {
		if now.Sub(pending.CreatedAt) > authCodeTTL {
			delete(s.pending, key)
		}
	}
	s.pendingMu.Unlock()

	s.codesMu.Lock()
	for key, code := range s.codes {
		if now.Sub(code.CreatedAt) > authCodeTTL || code.Used {
			delete(s.codes, key)
		}
	}
	s.codesMu.Unlock()
}

func (s *simpleService) storePendingAuth(state string, pending *pendingAuth) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	s.pending[state] = pending
}

func (s *simpleService) takePendingAuth(state string) (*pendingAuth, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	pending, ok := s.pending[state]
	if ok {
		delete(s.pending, state)
	}

	return pending, ok
}

func (s *simpleService) storeIssuedCode(code string, issued *issuedCode) {
	s.codesMu.Lock()
	defer s.codesMu.Unlock()

	s.codes[code] = issued
}

func (s *simpleService) consumeAuthorizationCode(
	code, clientID, redirectURI, resource, codeVerifier string,
) (*issuedCode, string) {
	s.codesMu.Lock()
	defer s.codesMu.Unlock()

	issued, ok := s.codes[code]
	if !ok {
		return nil, "invalid authorization code"
	}

	if issued.Used {
		return nil, "authorization code already used"
	}

	if time.Since(issued.CreatedAt) > authCodeTTL {
		return nil, "authorization code expired"
	}

	if issued.ClientID != clientID || issued.RedirectURI != redirectURI || issued.Resource != resource {
		return nil, "parameter mismatch"
	}

	if !s.verifyPKCE(codeVerifier, issued.CodeChallenge) {
		return nil, "invalid code_verifier"
	}

	issued.Used = true

	return issued, ""
}

func (s *simpleService) generateState() (string, error) {
	return s.generateRandomToken(32)
}

func (s *simpleService) generateRandomToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
