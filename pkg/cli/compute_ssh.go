package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	computeSSHIdentity string
	computeSSHPrint    bool
)

// sshPreparation mirrors the compute backend's SSHPreparation response.
type sshPreparation struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Username          string `json:"username"`
	KeyFingerprint    string `json:"key_fingerprint"`
	ClientCertificate string `json:"client_certificate"`
	ExpiresAt         string `json:"expires_at"`
}

var computeSandboxesSSHCmd = &cobra.Command{
	Use:   "ssh <id> [-- ssh-args...]",
	Short: "SSH into a running sandbox",
	Long: `SSH into a running sandbox through the compute SSH gateway.

Sends the public half of --identity to the control plane, which returns a
short-lived certificate for the matching registered key (register one with
'panda compute keys add'). The certificate is written next to nothing —
it lives under the user cache dir — and ssh is exec'd immediately since
certificates expire within minutes.

Arguments after -- are passed to ssh verbatim (e.g. a remote command).`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		identity, err := expandPath(computeSSHIdentity)
		if err != nil {
			return err
		}

		publicKey, err := os.ReadFile(identity + ".pub")
		if err != nil {
			return fmt.Errorf("reading public key: %w (register a key with 'panda compute keys add' and pass its private half via --identity)", err)
		}

		opArgs := computeIDArgs(args[0])
		opArgs["public_key"] = strings.TrimSpace(string(publicKey))

		response, err := runServerOperationRaw(cmd, "compute.prepare_sandbox_ssh", opArgs)
		if err != nil {
			return err
		}

		var prep sshPreparation
		if err := json.Unmarshal(response.Body, &prep); err != nil {
			return fmt.Errorf("decoding ssh preparation: %w", err)
		}

		if prep.Host == "" || prep.Username == "" || prep.ClientCertificate == "" {
			return fmt.Errorf("ssh preparation response is incomplete: %s", string(response.Body))
		}

		certPath, err := writeSSHCertificate(args[0], prep.ClientCertificate)
		if err != nil {
			return err
		}

		sshArgs := []string{
			"-i", identity,
			"-o", "CertificateFile=" + certPath,
			"-o", "IdentitiesOnly=yes",
			"-p", strconv.Itoa(prep.Port),
			prep.Username + "@" + prep.Host,
		}
		sshArgs = append(sshArgs, args[1:]...)

		if computeSSHPrint {
			cmd.Printf("ssh %s\n", strings.Join(sshArgs, " "))
			cmd.Printf("certificate expires at %s\n", prep.ExpiresAt)

			return nil
		}

		ssh := exec.CommandContext(commandContext(cmd), "ssh", sshArgs...)
		ssh.Stdin = os.Stdin
		ssh.Stdout = os.Stdout
		ssh.Stderr = os.Stderr

		return ssh.Run()
	},
}

// writeSSHCertificate stores the short-lived gateway certificate where ssh can
// read it, keyed by sandbox so parallel sessions do not clobber each other.
func writeSSHCertificate(sandboxID, certificate string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving cache dir: %w", err)
	}

	dir := filepath.Join(cacheDir, "panda", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating certificate dir: %w", err)
	}

	path := filepath.Join(dir, sandboxID+"-cert.pub")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(certificate)+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing certificate: %w", err)
	}

	return path, nil
}

func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home dir: %w", err)
		}

		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}

	return path, nil
}
