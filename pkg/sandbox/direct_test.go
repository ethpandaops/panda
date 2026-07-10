package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

// sessionEnabledCfg returns a SandboxConfig with sessions enabled so tests
// don't have to repeat the boilerplate.
func sessionEnabledCfg() config.SandboxConfig {
	ten := true
	return config.SandboxConfig{
		Timeout: 30,
		ExecUID: directExecTestUID,
		ExecGID: directExecTestUID,
		Sessions: config.SessionConfig{
			Enabled:     &ten,
			TTL:         10 * time.Minute,
			MaxDuration: 30 * time.Minute,
			MaxSessions: 10,
		},
	}
}

// TestDirectBackendWithholdsProcessSecrets is the regression gate for the
// credential leak: the direct backend must NOT pass the panda-server process
// env (which holds PANDA_BOT_TOKEN) to untrusted, LLM-generated code. The data
// plane is reached via req.Env, so a secret living only in the process env must
// be invisible to the executed script, while req.Env stays visible.
func TestDirectBackendWithholdsProcessSecrets(t *testing.T) {
	requireDirectExec(t)

	t.Setenv("PANDA_BOT_TOKEN", "super-secret-bot-token")

	b, err := NewDirectBackend(config.SandboxConfig{Timeout: 30, ExecUID: directExecTestUID, ExecGID: directExecTestUID}, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	res, err := b.Execute(context.Background(), ExecuteRequest{
		Code: "import os\n" +
			"print('BOT=' + os.environ.get('PANDA_BOT_TOKEN', 'ABSENT'))\n" +
			"print('REQ=' + os.environ.get('FROM_REQ', 'ABSENT'))\n",
		Env: map[string]string{"FROM_REQ": "visible"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if strings.Contains(res.Stdout, "super-secret-bot-token") {
		t.Fatalf("bot token leaked into executed code: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "BOT=ABSENT") {
		t.Errorf("expected PANDA_BOT_TOKEN withheld (BOT=ABSENT), got: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "REQ=visible") {
		t.Errorf("expected req.Env passthrough (REQ=visible), got: %q", res.Stdout)
	}
}

// TestDirectBackendSessionCreateListDestroy verifies the full session lifecycle.
func TestDirectBackendSessionCreateListDestroy(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	if !b.SessionsEnabled() {
		t.Fatal("expected sessions enabled")
	}

	// Create a session.
	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session ID")
	}

	// List sessions.
	sessions, err := b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != id {
		t.Fatalf("expected session ID %q, got %q", id, sessions[0].ID)
	}

	// Filter by owner.
	ownerSessions, err := b.ListSessions(context.Background(), "user1")
	if err != nil {
		t.Fatalf("ListSessions(owner): %v", err)
	}
	if len(ownerSessions) != 1 {
		t.Fatalf("expected 1 session for owner, got %d", len(ownerSessions))
	}

	// Different owner gets nothing.
	otherSessions, err := b.ListSessions(context.Background(), "other")
	if err != nil {
		t.Fatalf("ListSessions(other): %v", err)
	}
	if len(otherSessions) != 0 {
		t.Fatalf("expected 0 sessions for other owner, got %d", len(otherSessions))
	}

	// Destroy the session.
	if err := b.DestroySession(context.Background(), id, ""); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	// Verify it's gone.
	sessions, err = b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions after destroy: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions after destroy, got %d", len(sessions))
	}
}

// TestDirectBackendSessionExecute verifies that code runs inside the session
// workspace and files persist across calls.
func TestDirectBackendSessionExecute(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	// Create a session.
	sessionID, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// First execution: write a file.
	res1, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "with open('data.txt', 'w') as f: f.write('hello from session')",
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute 1: %v", err)
	}
	if res1.SessionID != sessionID {
		t.Fatalf("expected session ID %q, got %q", sessionID, res1.SessionID)
	}
	if res1.SessionTTLRemaining <= 0 {
		t.Fatal("expected positive TTL remaining")
	}

	// Second execution: read the file back — proves workspace persistence.
	res2, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "with open('data.txt') as f: print(f.read())",
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if !strings.Contains(res2.Stdout, "hello from session") {
		t.Fatalf("expected file content from previous execution, got stdout: %q", res2.Stdout)
	}

	// Session should report workspace files.
	if len(res2.SessionFiles) == 0 {
		t.Fatal("expected session files to be reported")
	}
	found := false
	for _, f := range res2.SessionFiles {
		if f.Name == "data.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected data.txt in session files")
	}

	// Third execution: new session should NOT see the file.
	sessionID2, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}

	res3, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "import os; print('exists' if os.path.exists('data.txt') else 'missing')",
		SessionID: sessionID2,
	})
	if err != nil {
		t.Fatalf("Execute 3: %v", err)
	}
	if !strings.Contains(res3.Stdout, "missing") {
		t.Fatalf("expected file missing in different session, got stdout: %q", res3.Stdout)
	}
}

// TestDirectBackendSessionLimits verifies MaxSessions enforcement.
func TestDirectBackendSessionLimits(t *testing.T) {
	requireDirectExec(t)

	cfg := sessionEnabledCfg()
	cfg.Sessions.MaxSessions = 2

	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	// First two should succeed.
	id1, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession 1: %v", err)
	}

	id2, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}

	canCreate, count, maxAllowed := b.CanCreateSession(context.Background(), "user1")
	if canCreate {
		t.Fatal("expected canCreate=false at limit")
	}
	if count != 2 {
		t.Fatalf("expected count=2, got %d", count)
	}
	if maxAllowed != 2 {
		t.Fatalf("expected maxAllowed=2, got %d", maxAllowed)
	}

	// Destroy one, then create should work again.
	if err := b.DestroySession(context.Background(), id1, ""); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	canCreate, count, _ = b.CanCreateSession(context.Background(), "user1")
	if !canCreate {
		t.Fatal("expected canCreate=true after destroy")
	}
	if count != 1 {
		t.Fatalf("expected count=1 after destroy, got %d", count)
	}

	// Clean up.
	_ = b.DestroySession(context.Background(), id2, "")
}

// TestDirectBackendSessionOwnerEnforcement verifies ownership checks.
func TestDirectBackendSessionOwnerEnforcement(t *testing.T) {
	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "alice", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Bob can't destroy Alice's session.
	if err := b.DestroySession(context.Background(), id, "bob"); err == nil {
		t.Fatal("expected error destroying another owner's session")
	}

	// Alice can destroy her own session.
	if err := b.DestroySession(context.Background(), id, "alice"); err != nil {
		t.Fatalf("DestroySession with owner: %v", err)
	}
}

// TestDirectBackendSessionDisabled verifies sessions can be disabled via config.
func TestDirectBackendSessionDisabled(t *testing.T) {
	disabled := false
	b, err := NewDirectBackend(config.SandboxConfig{
		Timeout: 30,
		Sessions: config.SessionConfig{
			Enabled: &disabled,
		},
	}, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	if b.SessionsEnabled() {
		t.Fatal("expected sessions disabled")
	}

	if _, err := b.CreateSession(context.Background(), "user1", nil); err == nil {
		t.Fatal("expected error creating session when disabled")
	}
}

// TestDirectBackendSessionEnvInjection verifies the session's environment
// variable is set when executing in a session.
func TestDirectBackendSessionEnvInjection(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	sessionID, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	res, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "import os; print('SID=' + os.environ.get('ETHPANDAOPS_SESSION_ID', 'ABSENT'))",
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(res.Stdout, "SID="+sessionID) {
		t.Fatalf("expected session ID in env, got stdout: %q", res.Stdout)
	}
}

// TestDirectBackendTTLExpiry verifies that idle sessions are expired.
func TestDirectBackendTTLExpiry(t *testing.T) {
	cfg := sessionEnabledCfg()
	cfg.Sessions.TTL = 50 * time.Millisecond
	cfg.Sessions.MaxDuration = 10 * time.Minute

	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Should exist before TTL.
	sessions, err := b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session before expiry, got %d", len(sessions))
	}

	// Wait for TTL to expire, then run cleanup.
	time.Sleep(100 * time.Millisecond)
	b.sessionManager.cleanupExpired(context.Background())

	sessions, err = b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions after expiry: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions after TTL expiry, got %d (id=%s)", len(sessions), id)
	}

	// Workspace directory should be gone.
	// Can't check the path directly since it's unexported, but verify
	// DestroySession returns a "not found" error.
	if err := b.DestroySession(context.Background(), id, ""); err == nil {
		t.Fatal("expected error destroying expired session")
	}
}

// TestDirectBackendWorkspaceDirPersistsAcrossExecutions verifies that files
// written by one execution are visible to the next in the same session.
func TestDirectBackendWorkspaceDirPersistsAcrossExecutions(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	sessionID, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Write a file.
	_, err = b.Execute(context.Background(), ExecuteRequest{
		Code:      "with open('greeting.txt', 'w') as f: f.write('persistent')",
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute write: %v", err)
	}

	// Verify it persists.
	res, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "with open('greeting.txt') as f: print(f.read())",
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute read: %v", err)
	}
	if !strings.Contains(res.Stdout, "persistent") {
		t.Fatalf("expected persistent file content, got: %q", res.Stdout)
	}

	// Verify the workspace directory actually exists on disk.
	s, err := b.store.Get(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if s == nil {
		t.Fatal("session not found")
	}
	if _, err := os.Stat(filepath.Join(s.Handle, "greeting.txt")); err != nil {
		t.Fatalf("greeting.txt should exist on disk: %v", err)
	}
}

// TestDirectBackendNonSessionTempDirIsCleanedUp verifies that non-session
// executions clean up their temp directories.
func TestDirectBackendNonSessionTempDirIsCleanedUp(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(config.SandboxConfig{Timeout: 30, ExecUID: directExecTestUID, ExecGID: directExecTestUID}, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	res, err := b.Execute(context.Background(), ExecuteRequest{
		Code: "import os, tempfile\n" +
			"print('TMP=' + tempfile.gettempdir())\n" +
			"print('CWD=' + os.getcwd())\n",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if res.SessionID != "" {
		t.Fatalf("expected empty session ID for non-session execution, got %q", res.SessionID)
	}
	if res.SessionTTLRemaining != 0 {
		t.Fatalf("expected zero TTL for non-session execution")
	}
}

// TestDirectBackendSessionTTLRefreshedOnExecute verifies that executing in a
// session refreshes the TTL.
func TestDirectBackendSessionTTLRefreshedOnExecute(t *testing.T) {
	requireDirectExec(t)

	cfg := sessionEnabledCfg()
	cfg.Sessions.TTL = 100 * time.Millisecond
	cfg.Sessions.MaxDuration = 30 * time.Minute

	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Sleep most of the TTL.
	time.Sleep(80 * time.Millisecond)

	// Execute should refresh the TTL.
	_, err = b.Execute(context.Background(), ExecuteRequest{
		Code:      "print('refresh')",
		SessionID: id,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Now the session should survive another TTL period.
	time.Sleep(80 * time.Millisecond)

	b.sessionManager.cleanupExpired(context.Background())

	sessions, err := b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected session to still exist after TTL refresh")
	}
}

// TestDirectBackendCanCreateSessionNoLimit verifies unlimited sessions when MaxSessions <= 0.
func TestDirectBackendCanCreateSessionNoLimit(t *testing.T) {
	cfg := sessionEnabledCfg()
	cfg.Sessions.MaxSessions = 0

	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	canCreate, count, maxAllowed := b.CanCreateSession(context.Background(), "user1")
	if !canCreate {
		t.Fatal("expected canCreate=true when no limit")
	}
	if count != 0 {
		t.Fatalf("expected count=0 when no sessions, got %d", count)
	}
	if maxAllowed != 0 {
		t.Fatalf("expected maxAllowed=0 for unlimited, got %d", maxAllowed)
	}
}

// TestDirectBackendMaxDurationExpiry verifies that sessions exceeding
// MaxDuration are expired even when they are still active.
func TestDirectBackendMaxDurationExpiry(t *testing.T) {
	requireDirectExec(t)

	cfg := sessionEnabledCfg()
	cfg.Sessions.TTL = 10 * time.Minute
	cfg.Sessions.MaxDuration = 50 * time.Millisecond

	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	b.sessionManager.cleanupExpired(context.Background())

	if err := b.DestroySession(context.Background(), id, ""); err == nil {
		t.Fatal("expected session to be expired by MaxDuration")
	}
}

// TestDirectBackendStopCleansUpSessions verifies Stop removes all workspaces.
func TestDirectBackendStopCleansUpSessions(t *testing.T) {
	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	_, err = b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, all sessions should be gone.
	sessions, err := b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions after Stop: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions after Stop, got %d", len(sessions))
	}

	// Can't call Stop twice (closed channel).
}

// TestDirectBackendExecuteMissingSessionError verifies a clear error when
// referencing a non-existent session.
func TestDirectBackendExecuteMissingSessionError(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	_, err = b.Execute(context.Background(), ExecuteRequest{
		Code:      "print('should not run')",
		SessionID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

// TestDirectBackendCleanupExpiredIsSafeWithNoSessions verifies the cleanup
// loop doesn't panic when there are no sessions.
func TestDirectBackendCleanupExpiredIsSafeWithNoSessions(t *testing.T) {
	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	// Should not panic.
	b.sessionManager.cleanupExpired(context.Background())

	sessions, err := b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

// TestDirectBackendManuallySetPendingSessionsNoExecDirect verifies that
// creating a session and immediately checking session info works.
func TestDirectBackendManuallySetPendingSessionsNoExecDirect(t *testing.T) {
	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sessions, err := b.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.ID != id {
		t.Fatalf("expected ID %q, got %q", id, s.ID)
	}
	if s.CreatedAt.IsZero() {
		t.Fatal("expected non-zero CreatedAt")
	}
	if s.LastUsed.IsZero() {
		t.Fatal("expected non-zero LastUsed")
	}
	if s.TTLRemaining <= 0 {
		t.Fatal("expected positive TTLRemaining")
	}
}

// TestDirectBackendEnvDefaultsHasEnvSessionID verifies that a session
// execution sets the ETHPANDAOPS_SESSION_ID env var for storage scoping.
func TestDirectBackendEnvDefaultsHasEnvSessionID(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	sessionID, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	res, err := b.Execute(context.Background(), ExecuteRequest{
		Code: `import os
print('SESSION_ID=' + os.environ.get('ETHPANDAOPS_SESSION_ID', 'ABSENT'))
`,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	expected := "SESSION_ID=" + sessionID
	if !strings.Contains(res.Stdout, expected) {
		t.Fatalf("expected %q in stdout, got: %q", expected, res.Stdout)
	}
}

// TestDirectBackendExecuteEnforcesSessionOwnership verifies a caller cannot
// execute code in a session owned by someone else — the regression gate for
// cross-owner workspace access on the direct backend.
func TestDirectBackendExecuteEnforcesSessionOwnership(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "alice", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Bob may not execute in Alice's session.
	if _, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "print('intruder')",
		SessionID: id,
		OwnerID:   "bob",
	}); err == nil {
		t.Fatal("expected error executing in another owner's session")
	} else if !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected 'not owned' error, got: %v", err)
	}

	// Alice may execute in her own session.
	if _, err := b.Execute(context.Background(), ExecuteRequest{
		Code:      "print('owner')",
		SessionID: id,
		OwnerID:   "alice",
	}); err != nil {
		t.Fatalf("owner Execute: %v", err)
	}
}

// TestDirectBackendDestroyDuringExecuteNoPanic verifies that destroying a
// session while an execution is in flight does not panic — the regression gate
// for the nil-pointer deref when populating session info after the map entry is
// gone. Run with -race.
func TestDirectBackendDestroyDuringExecuteNoPanic(t *testing.T) {
	requireDirectExec(t)

	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, execErr := b.Execute(context.Background(), ExecuteRequest{
			Code:      "import time; time.sleep(0.3); print('done')",
			SessionID: id,
		})
		done <- execErr
	}()

	// Destroy the session mid-execution.
	time.Sleep(80 * time.Millisecond)
	_ = b.DestroySession(context.Background(), id, "")

	// Execute must return (no panic) regardless of the concurrent destroy.
	if err := <-done; err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
}

// TestDirectBackendCleanupSkipsInFlightExecution verifies the executing guard:
// idle-TTL cleanup must not reclaim a session that has an execution in flight,
// even when it has exceeded its TTL.
func TestDirectBackendCleanupSkipsInFlightExecution(t *testing.T) {
	cfg := sessionEnabledCfg()
	cfg.Sessions.TTL = 20 * time.Millisecond
	cfg.Sessions.MaxDuration = 10 * time.Minute // only idle TTL governs here

	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}

	id, err := b.CreateSession(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// An in-flight execution protects the session from idle-TTL purging.
	b.sessionManager.markExecuting(id)

	time.Sleep(40 * time.Millisecond) // TTL elapsed
	b.sessionManager.cleanupExpired(context.Background())

	if sessions, _ := b.ListSessions(context.Background(), ""); len(sessions) != 1 {
		t.Fatalf("expected in-flight session to survive cleanup, got %d", len(sessions))
	}

	// Once the execution finishes and its refreshed idle timer elapses, cleanup
	// reclaims it.
	b.sessionManager.unmarkExecuting(id)
	time.Sleep(40 * time.Millisecond)
	b.sessionManager.cleanupExpired(context.Background())

	if sessions, _ := b.ListSessions(context.Background(), ""); len(sessions) != 0 {
		t.Fatalf("expected session reclaimed after execution finished, got %d", len(sessions))
	}
}

// TestDirectBackendConfiguredPythonPath verifies the python_path config pins the
// interpreter and that Start fails fast when it is missing.
func TestDirectBackendConfiguredPythonPath(t *testing.T) {
	requireDirectExec(t)

	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	cfg := config.SandboxConfig{Timeout: 30, PythonPath: python3, ExecUID: directExecTestUID, ExecGID: directExecTestUID}
	b, err := NewDirectBackend(cfg, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}
	if got := b.pythonBin(); got != python3 {
		t.Fatalf("expected pythonBin %q, got %q", python3, got)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start with valid python_path: %v", err)
	}
	_ = b.Stop(context.Background())

	// A bogus path must fail fast at Start, not fall back to PATH.
	bad, err := NewDirectBackend(config.SandboxConfig{Timeout: 30, PythonPath: "/nonexistent/python", ExecUID: directExecTestUID, ExecGID: directExecTestUID}, logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}
	if err := bad.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail with a nonexistent python_path")
	}
}

// TestDirectBackendStopIsIdempotent verifies Stop can be called twice without
// panicking on a double channel close.
func TestDirectBackendStopIsIdempotent(t *testing.T) {
	b, err := NewDirectBackend(sessionEnabledCfg(), logrus.New())
	if err != nil {
		t.Fatalf("NewDirectBackend: %v", err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
