package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingAuthStoreRoundTrip(t *testing.T) {
	svc := newTestSimpleService(t)
	pending := &pendingAuth{ClientID: "panda", CreatedAt: time.Now()}

	svc.storePendingAuth("state-1", pending)

	got, ok := svc.takePendingAuth("state-1")
	require.True(t, ok)
	require.Same(t, pending, got)

	got, ok = svc.takePendingAuth("state-1")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestCleanupRemovesExpiredAndUsedEntries(t *testing.T) {
	svc := newTestSimpleService(t)
	expiredAt := time.Now().Add(-2 * authCodeTTL)

	svc.storePendingAuth("fresh", &pendingAuth{CreatedAt: time.Now()})
	svc.storePendingAuth("expired", &pendingAuth{CreatedAt: expiredAt})
	svc.storeIssuedCode("fresh", &issuedCode{CreatedAt: time.Now()})
	svc.storeIssuedCode("used", &issuedCode{CreatedAt: time.Now(), Used: true})
	svc.storeIssuedCode("expired", &issuedCode{CreatedAt: expiredAt})

	svc.cleanup()

	svc.pendingMu.RLock()
	_, freshPending := svc.pending["fresh"]
	_, expiredPending := svc.pending["expired"]
	svc.pendingMu.RUnlock()
	assert.True(t, freshPending)
	assert.False(t, expiredPending)

	svc.codesMu.RLock()
	_, freshCode := svc.codes["fresh"]
	_, usedCode := svc.codes["used"]
	_, expiredCode := svc.codes["expired"]
	svc.codesMu.RUnlock()
	assert.True(t, freshCode)
	assert.False(t, usedCode)
	assert.False(t, expiredCode)
}

func TestGenerateRandomTokenAndConsumeAuthorizationCode(t *testing.T) {
	svc := newTestSimpleService(t)

	token, err := svc.generateRandomToken(8)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	verifier := "verifier-123"
	challenge := sha256.Sum256([]byte(verifier))
	svc.storeIssuedCode("code-1", &issuedCode{
		Code:          "code-1",
		ClientID:      "panda",
		RedirectURI:   "http://localhost:8085/callback",
		Resource:      "http://proxy.test",
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]),
		CreatedAt:     time.Now(),
	})

	issued, message := svc.consumeAuthorizationCode(
		"code-1",
		"panda",
		"http://localhost:8085/callback",
		"http://proxy.test",
		verifier,
	)
	assert.Empty(t, message)
	require.NotNil(t, issued)
	assert.True(t, issued.Used)
}
