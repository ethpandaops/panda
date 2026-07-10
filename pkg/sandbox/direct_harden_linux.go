//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ethpandaops/panda/pkg/config"
)

// This file implements the OS-level confinement for the direct backend. The
// executed Python is LLM-generated and untrusted, and the direct backend runs
// it as a child of panda-server with no container between them. Withholding the
// server env (see direct.go) closes only the inherited-environment channel; the
// filesystem and /proc remain reachable at the server's uid. We close those with
// four layers, all applied to the child before it execs Python:
//
//  1. uid/gid drop — run Python as a dedicated unprivileged id (ExecUID/ExecGID)
//     that owns none of the server's secrets, so config.yaml and the on-disk
//     OAuth/credential files (0600, owned by the server uid) become unreadable
//     and /proc/<server-pid>/{environ,mem} fail ptrace_may_access.
//  2. PID namespace — the child cannot even see the server process in /proc.
//  3. mount namespace + fresh /proc — private mounts, and a procfs bound to the
//     child's PID namespace so it reflects only the child.
//  4. Landlock — the filesystem is restricted to the workspace plus the minimal
//     read/exec set Python needs; config and credential paths are simply absent.
//
// Because Landlock and mount() cannot be expressed through exec.Cmd.SysProcAttr,
// we re-exec panda-server itself as a tiny trampoline (directSandboxInitArg):
// the trampoline runs inside the new namespaces while still privileged, sets up
// mounts + Landlock, drops to the sandbox uid, then execs Python. This is the
// runc-style "init" pattern. Any failure in the trampoline aborts before Python
// runs, so a broken confinement fails closed (no execution) rather than open.

const (
	// directSandboxInitArg is the argv[1] that re-invokes panda-server as the
	// in-namespace trampoline. It must not collide with any cobra subcommand.
	directSandboxInitArg = "__direct-sandbox-init"

	// Control variables carry the trampoline's parameters out-of-band from the
	// Python target env. The trampoline strips them before exec'ing Python.
	envCtlUID     = "__PANDA_SB_UID"
	envCtlGID     = "__PANDA_SB_GID"
	envCtlWorkdir = "__PANDA_SB_WORKDIR"
	envCtlPython  = "__PANDA_SB_PYTHON"
	envCtlScript  = "__PANDA_SB_SCRIPT"
)

// Landlock ABI-1 filesystem access rights and syscall constants. x/sys exports
// the access-right bits but not the rule type / ruleset-attr helpers, so the
// syscalls are issued directly.
const (
	landlockRulePathBeneath      = 1
	landlockCreateRulesetVersion = 1 << 0

	// Union of every ABI-1 FS right. Handling all of them means any right we do
	// not explicitly grant on a path is denied there.
	llReadFile  = unix.LANDLOCK_ACCESS_FS_READ_FILE
	llReadDir   = unix.LANDLOCK_ACCESS_FS_READ_DIR
	llWriteFile = unix.LANDLOCK_ACCESS_FS_WRITE_FILE
	llExecute   = unix.LANDLOCK_ACCESS_FS_EXECUTE
	llAllABI1   = llExecute | llWriteFile | llReadFile | llReadDir |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
)

// landlockRulesetAttr mirrors struct landlock_ruleset_attr (ABI 1: a single
// handled_access_fs field). Passing an 8-byte size keeps it valid on every ABI.
type landlockRulesetAttr struct {
	handledAccessFS uint64
}

// landlockPathBeneathAttr mirrors struct landlock_path_beneath_attr. The kernel
// reads the packed 12 bytes (u64 + s32); Go's trailing alignment padding is
// past that and ignored.
type landlockPathBeneathAttr struct {
	allowedAccess uint64
	parentFd      int32
}

// pathRule is one Landlock allowlist entry: a path and the rights granted on it.
type pathRule struct {
	path    string
	access  uint64
	require bool // fail closed if a required path is missing; skip optional ones
}

// newHardenedSandboxCmd builds the exec.Cmd that runs untrusted Python under the
// full confinement stack. It targets /proc/self/exe (the trampoline) inside a
// fresh mount + PID namespace; the trampoline finishes the setup and execs
// Python. targetEnv is the environment Python receives (never os.Environ).
func newHardenedSandboxCmd(ctx context.Context, workDir, scriptPath, pythonBin string, uid, gid int, targetEnv []string) *exec.Cmd {
	// CommandContext wires the timeout: on ctx expiry the trampoline (and the
	// Python it exec'd, its only descendant) is SIGKILLed.
	cmd := exec.CommandContext(ctx, "/proc/self/exe", directSandboxInitArg)
	cmd.Args[0] = "panda-sandbox-init"

	env := make([]string, 0, len(targetEnv)+5)
	env = append(env, targetEnv...)
	env = append(env,
		envCtlUID+"="+strconv.Itoa(uid),
		envCtlGID+"="+strconv.Itoa(gid),
		envCtlWorkdir+"="+workDir,
		envCtlPython+"="+pythonBin,
		envCtlScript+"="+scriptPath,
	)
	cmd.Env = env

	cmd.SysProcAttr = &syscall.SysProcAttr{
		// New mount + PID namespaces (needs CAP_SYS_ADMIN, verified by preflight).
		// The uid drop happens inside the trampoline after mounting, so the caps
		// survive to that point; Credential is deliberately NOT set here.
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		Pdeathsig:  syscall.SIGKILL,
	}

	return cmd
}

// RunDirectSandboxInitIfRequested runs the in-namespace trampoline when this
// process was re-exec'd as one, and never returns in that case. It is a no-op
// (returns false) for a normal server invocation. Call it before any flag/config
// parsing — the trampoline's argv is not a cobra command.
func RunDirectSandboxInitIfRequested() bool {
	if len(os.Args) < 2 || os.Args[1] != directSandboxInitArg {
		return false
	}

	if err := directSandboxInit(); err != nil {
		// Fail closed: Python never execs, the parent sees this on stderr.
		fmt.Fprintf(os.Stderr, "direct-sandbox-init: %v\n", err)
		os.Exit(126)
	}

	// directSandboxInit execs on success and does not return.
	os.Exit(127)

	return true
}

// directSandboxInit runs inside the new namespaces, still holding the caps
// inherited (ambiently) from panda-server. It seals the environment and execs
// Python. Every step is checked; any error propagates and fails closed.
func directSandboxInit() error {
	// Pin to one OS thread for the whole sequence. The mount, Landlock,
	// no_new_privs, and uid-drop syscalls are all per-thread, and execve carries
	// the calling thread's credentials + restrictions. Without this the goroutine
	// could migrate to a thread that never dropped and execve Python as root.
	// (We never unlock: execve replaces the image, destroying all other threads.)
	runtime.LockOSThread()

	uid, err := strconv.Atoi(os.Getenv(envCtlUID))
	if err != nil || uid <= 0 {
		return fmt.Errorf("invalid %s: %q", envCtlUID, os.Getenv(envCtlUID))
	}

	gid, err := strconv.Atoi(os.Getenv(envCtlGID))
	if err != nil || gid <= 0 {
		return fmt.Errorf("invalid %s: %q", envCtlGID, os.Getenv(envCtlGID))
	}

	workDir := os.Getenv(envCtlWorkdir)
	pythonBin := os.Getenv(envCtlPython)
	scriptPath := os.Getenv(envCtlScript)
	if workDir == "" || pythonBin == "" || scriptPath == "" {
		return fmt.Errorf("missing control vars (workdir=%q python=%q script=%q)", workDir, pythonBin, scriptPath)
	}

	// Isolate the mount namespace so our /proc remount cannot propagate to the
	// host, then bind a fresh procfs to the new PID namespace so it reflects only
	// this process — the server's /proc entries become invisible.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("making mounts private: %w", err)
	}

	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mounting fresh /proc: %w", err)
	}

	if err := unix.Chdir(workDir); err != nil {
		return fmt.Errorf("chdir to workspace: %w", err)
	}

	// no_new_privs before Landlock (required for the restrict_self path) and
	// before the uid drop, so no suid binary the sandbox execs can regain privs.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}

	if err := applyLandlock(workDir, pythonBin); err != nil {
		return fmt.Errorf("applying landlock: %w", err)
	}

	// Drop privileges last: supplementary groups, then gid, then uid. After this
	// the process holds no capabilities and cannot reach the server's secrets.
	if err := unix.Setgroups([]int{gid}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}

	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return fmt.Errorf("setresgid: %w", err)
	}

	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return fmt.Errorf("setresuid: %w", err)
	}

	if os.Getuid() != uid || os.Geteuid() != uid {
		return fmt.Errorf("uid drop did not stick (uid=%d euid=%d want=%d)", os.Getuid(), os.Geteuid(), uid)
	}

	pythonEnv := strippedEnv()

	if err := unix.Exec(pythonBin, []string{pythonBin, scriptPath}, pythonEnv); err != nil {
		return fmt.Errorf("exec %s: %w", pythonBin, err)
	}

	return nil // unreachable
}

// strippedEnv returns the current environment without the control variables, so
// Python sees exactly the env the server built for it.
func strippedEnv() []string {
	all := os.Environ()
	out := make([]string, 0, len(all))
	for _, kv := range all {
		switch {
		case strings.HasPrefix(kv, envCtlUID+"="),
			strings.HasPrefix(kv, envCtlGID+"="),
			strings.HasPrefix(kv, envCtlWorkdir+"="),
			strings.HasPrefix(kv, envCtlPython+"="),
			strings.HasPrefix(kv, envCtlScript+"="):
			continue
		}
		out = append(out, kv)
	}

	return out
}

// applyLandlock confines the process filesystem to the workspace (read/write)
// plus the minimal read/exec paths a Python interpreter needs. Everything else —
// the server config, the credential store, arbitrary /home and /app — is denied
// because it is never granted. /proc is granted read (Python and the Go runtime
// need /proc/self) which is safe: the PID namespace already hides other
// processes and the server marks itself non-dumpable.
func applyLandlock(workDir, pythonBin string) error {
	rules := []pathRule{
		{path: workDir, access: llAllABI1, require: true},

		// Interpreter + shared libraries + stdlib. pythonVenvRoot climbs to the
		// venv/prefix root so the whole install is readable+executable.
		{path: pythonVenvRoot(pythonBin), access: llReadFile | llReadDir | llExecute, require: true},
		{path: "/usr", access: llReadFile | llReadDir | llExecute, require: false},
		{path: "/lib", access: llReadFile | llReadDir | llExecute, require: false},
		{path: "/lib64", access: llReadFile | llReadDir | llExecute, require: false},
		{path: "/bin", access: llReadFile | llReadDir | llExecute, require: false},
		{path: "/sbin", access: llReadFile | llReadDir | llExecute, require: false},
		{path: "/opt", access: llReadFile | llReadDir | llExecute, require: false},

		// procfs (see doc comment) and the few device nodes Python opens.
		{path: "/proc", access: llReadFile | llReadDir, require: false},
		{path: "/sys/kernel/mm/transparent_hugepage", access: llReadFile | llReadDir, require: false},
		{path: "/dev/null", access: llReadFile | llWriteFile, require: false},
		{path: "/dev/zero", access: llReadFile, require: false},
		{path: "/dev/random", access: llReadFile, require: false},
		{path: "/dev/urandom", access: llReadFile, require: false},

		// TLS roots, name resolution, timezone, and passwd/group lookups. These
		// are single world-readable files, not the secret-bearing config paths.
		{path: "/etc/ssl", access: llReadFile | llReadDir, require: false},
		{path: "/etc/ca-certificates", access: llReadFile | llReadDir, require: false},
		{path: "/etc/pki", access: llReadFile | llReadDir, require: false},
		{path: "/etc/resolv.conf", access: llReadFile, require: false},
		{path: "/etc/hosts", access: llReadFile, require: false},
		{path: "/etc/host.conf", access: llReadFile, require: false},
		{path: "/etc/nsswitch.conf", access: llReadFile, require: false},
		{path: "/etc/gai.conf", access: llReadFile, require: false},
		{path: "/etc/passwd", access: llReadFile, require: false},
		{path: "/etc/group", access: llReadFile, require: false},
		{path: "/etc/localtime", access: llReadFile, require: false},
	}

	attr := landlockRulesetAttr{handledAccessFS: llAllABI1}

	rulesetFd, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		unsafe.Sizeof(attr),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(rulesetFd)) }()

	for _, r := range rules {
		if err := landlockAddPath(int(rulesetFd), r); err != nil {
			return err
		}
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs before restrict_self: %w", err)
	}

	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rulesetFd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}

	return nil
}

// landlockAddPath adds one path_beneath rule. A missing optional path is
// skipped; a missing required path fails closed.
func landlockAddPath(rulesetFd int, r pathRule) error {
	fd, err := unix.Open(r.path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if !r.require && (err == unix.ENOENT || err == unix.EACCES) {
			return nil
		}

		return fmt.Errorf("open %s for landlock rule: %w", r.path, err)
	}
	defer func() { _ = unix.Close(fd) }()

	attr := landlockPathBeneathAttr{allowedAccess: r.access, parentFd: int32(fd)}

	if _, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd),
		landlockRulePathBeneath,
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	); errno != 0 {
		return fmt.Errorf("landlock_add_rule for %s: %w", r.path, errno)
	}

	return nil
}

// pythonVenvRoot returns the install root to grant read+exec for the given
// interpreter: for .../bin/python it is the parent of bin; otherwise the
// interpreter's own directory.
func pythonVenvRoot(pythonBin string) string {
	dir := parentDir(pythonBin)
	if base(dir) == "bin" {
		return parentDir(dir)
	}

	return dir
}

func parentDir(p string) string {
	i := strings.LastIndex(strings.TrimRight(p, "/"), "/")
	if i <= 0 {
		return "/"
	}

	return p[:i]
}

func base(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}

	return p
}

// preflightDirectHardening verifies, at server startup, that every confinement
// primitive the direct backend relies on is actually available, and fails closed
// otherwise. It never weakens the backend to "best effort".
func preflightDirectHardening(cfg config.SandboxConfig) error {
	self := os.Getuid()

	if cfg.ExecUID <= 0 || cfg.ExecGID <= 0 {
		return fmt.Errorf("sandbox.exec_uid/exec_gid must be set (non-zero) for the direct backend")
	}

	if cfg.ExecUID == self {
		return fmt.Errorf("sandbox.exec_uid (%d) must differ from the server uid (%d); running Python as the server uid defeats the isolation", cfg.ExecUID, self)
	}

	// Namespaces + uid drop need CAP_SYS_ADMIN, CAP_SETUID, CAP_SETGID. Root has
	// them implicitly; a non-root server must carry them (ambient caps, see the
	// entrypoint). Verify the effective set so a misconfigured deployment fails
	// at boot, not at first execution.
	if self != 0 {
		eff, err := effectiveCaps()
		if err != nil {
			return fmt.Errorf("reading effective capabilities: %w", err)
		}

		for _, c := range []struct {
			bit  uint
			name string
		}{
			{unix.CAP_SYS_ADMIN, "CAP_SYS_ADMIN"},
			{unix.CAP_SETUID, "CAP_SETUID"},
			{unix.CAP_SETGID, "CAP_SETGID"},
		} {
			if eff&(1<<c.bit) == 0 {
				return fmt.Errorf("direct backend requires %s (server runs as uid %d without it); grant it via the pod securityContext and ambient caps", c.name, self)
			}
		}
	}

	if v := landlockABIVersion(); v < 1 {
		return fmt.Errorf("landlock is unavailable (abi=%d); the direct backend requires a Linux 5.13+ kernel with landlock enabled", v)
	}

	return nil
}

// landlockABIVersion returns the kernel's Landlock ABI version, or a value < 1
// when Landlock is unavailable.
func landlockABIVersion() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return -1
	}

	return int(v)
}

// effectiveCaps reads the effective capability set of the current process from
// /proc/self/status (CapEff).
func effectiveCaps() (uint64, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
		}
	}

	return 0, fmt.Errorf("CapEff not found in /proc/self/status")
}

// setServerNonDumpable marks panda-server non-dumpable so its /proc/<pid>/
// {environ,mem,maps} become root-owned and unreadable to same-uid processes —
// defense in depth for the /proc channel independent of the host ptrace_scope.
func setServerNonDumpable() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
