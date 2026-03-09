package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/mcp/pkg/serverapi"
)

var (
	executeCode    string
	executeFile    string
	executeTimeout int
	executeSession string
	executeJSON    bool
)

var executeCmd = &cobra.Command{
	Use:   "execute",
	Short: "Execute Python code in a sandbox",
	Long: `Execute Python code in an isolated sandbox container with access to
the ethpandaops library for Ethereum data analysis. All data access
flows through the credential proxy.

Code can be provided via --code, --file, or stdin.

Examples:
  ep execute --code 'print("hello")'
  ep execute --file script.py
  ep execute --file script.py --session abc123
  echo 'print("hello")' | ep execute
  ep execute --json --code 'import pandas; print(pandas.__version__)'`,
	RunE: runExecute,
}

func init() {
	rootCmd.AddCommand(executeCmd)
	executeCmd.Flags().StringVar(&executeCode, "code", "", "Python code to execute")
	executeCmd.Flags().StringVar(&executeFile, "file", "", "Path to Python file to execute")
	executeCmd.Flags().IntVar(&executeTimeout, "timeout", 0, "Execution timeout in seconds (default: from config)")
	executeCmd.Flags().StringVar(&executeSession, "session", "", "Session ID to reuse")
	executeCmd.Flags().BoolVar(&executeJSON, "json", false, "Output result as JSON")
}

func runExecute(_ *cobra.Command, _ []string) error {
	code, err := resolveCode()
	if err != nil {
		return err
	}

	ctx := context.Background()
	result, err := executeCodeRemotely(ctx, serverapi.ExecuteRequest{
		Code:      code,
		Timeout:   executeTimeout,
		SessionID: executeSession,
	})
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	if executeJSON {
		return printJSON(result)
	}

	// Print stdout to stdout, stderr to stderr.
	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}

	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}

	// Print metadata to stderr so stdout stays clean.
	if len(result.OutputFiles) > 0 {
		fmt.Fprintf(os.Stderr, "[files] %s\n", strings.Join(result.OutputFiles, ", "))
	}

	if result.SessionID != "" {
		ttl := result.SessionTTLRemaining
		if ttl == "" {
			ttl = "unknown"
		}
		fmt.Fprintf(os.Stderr, "[session] %s (ttl: %s)\n", result.SessionID, ttl)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("exit code %d", result.ExitCode)
	}

	return nil
}

func resolveCode() (string, error) {
	switch {
	case executeCode != "":
		return executeCode, nil
	case executeFile != "":
		data, err := os.ReadFile(executeFile)
		if err != nil {
			return "", fmt.Errorf("reading file: %w", err)
		}

		return string(data), nil
	default:
		// Check if stdin has data.
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			return "", fmt.Errorf("provide code via --code, --file, or stdin")
		}

		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}

		if len(data) == 0 {
			return "", fmt.Errorf("no code provided")
		}

		return string(data), nil
	}
}
