package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

var (
	executeCode    string
	executeFile    string
	executeTimeout int
	executeSession string
)

// exitCodeError carries a sandbox exit code so the process can mirror it as its
// own exit status instead of collapsing every failure to 1.
type exitCodeError struct {
	code int
}

func (e *exitCodeError) Error() string {
	return fmt.Sprintf("exit code %d", e.code)
}

var executeCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "execute",
	Short:   "Execute Python code in a sandbox",
	Long: `Execute Python code in an isolated sandbox container with access to
the ethpandaops library for Ethereum data analysis. All data access
flows through the credential proxy.

Code can be provided via --code, --file, or stdin.
Use --code for short one-liners. For multiline Python, prefer --file or stdin
so shell quoting does not change the program.

For a single SQL or PromQL answer, direct datasource commands are usually
simpler and avoid Python quoting/session overhead:
  panda clickhouse query-raw <datasource> "<SQL>"
  panda prometheus query <datasource> "<promql>"

Use execute when you need Python libraries, files, plots, cross-source joins,
or multi-step analysis.

Examples:
  panda execute --code 'print("hello")'
  panda execute --file script.py
  panda execute --file script.py --session abc123
  panda execute < script.py
  echo 'print("hello")' | panda execute
  panda execute --json --code 'import pandas; print(pandas.__version__)'`,
	RunE: runExecute,
}

func init() {
	rootCmd.AddCommand(executeCmd)
	executeCmd.Flags().StringVar(&executeCode, "code", "", "Python code to execute")
	executeCmd.Flags().StringVar(&executeFile, "file", "", "Path to Python file to execute")
	executeCmd.Flags().IntVar(&executeTimeout, "timeout", 0, "Execution timeout in seconds (default: from config)")
	executeCmd.Flags().StringVar(&executeSession, "session", "", "Session ID to reuse")

	_ = executeCmd.RegisterFlagCompletionFunc("file", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"py"}, cobra.ShellCompDirectiveFilterFileExt
	})
	_ = executeCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
}

func runExecute(cmd *cobra.Command, _ []string) error {
	code, err := resolveCode()
	if err != nil {
		return err
	}

	result, err := executeCodeRemotely(cmd.Context(), serverapi.ExecuteRequest{
		Code:      code,
		Timeout:   executeTimeout,
		SessionID: executeSession,
	})
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	if isJSON() {
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
		return &exitCodeError{code: result.ExitCode}
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
