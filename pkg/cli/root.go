// Package cli provides the command-line interface for ethpandaops Ethereum analytics.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/internal/github"
	"github.com/ethpandaops/panda/internal/version"
)

// Command group IDs for cobra help grouping.
const (
	groupWorkflow  = "workflow"
	groupDiscovery = "discovery"
	groupDirect    = "direct"
	groupSetup     = "setup"
)

var (
	cfgFile  string
	logLevel string
	log      = logrus.New()
)

const rootLong = `Ethereum network analytics CLI.

For data questions, follow the discovery workflow instead of guessing commands,
tables, columns, or query syntax:

  1. Find available data: panda datasets, panda datasources, panda schema
  2. Search prior art: panda search examples "<topic>"
  3. Search procedures when the task is multi-step: panda search runbooks "<topic>"
  4. Search protocol constants/specs: panda search consensus-specs "<topic>"
  5. Read dataset rules when a search result names one: panda datasets <name>
  6. Execute with the narrowest command that fits, or use panda execute

Most topic words are search terms, not subcommands. Full guide:

  panda getting-started

Not a data question? Drive the workflow engine with ` + "`panda workflow`" + ` (authoring
and running multi-step agent workflows), not Ethereum data queries.`

// updateResult carries the latest version from the background check.
// A nil value means the check failed or was skipped.
var updateResult = make(chan *string, 1)

// skipUpdateCheckCommands lists commands that should not trigger
// update checks or display update notifications. query-raw is a machine
// surface whose consumers routinely merge stderr into JSON pipelines, so
// even advisory chatter breaks them.
var skipUpdateCheckCommands = map[string]bool{
	"upgrade":    true,
	"version":    true,
	"completion": true,
	"init":       true,
	"help":       true,
	"query-raw":  true,
}

var rootCmd = &cobra.Command{
	Use:     "panda",
	Short:   "Ethereum network analytics CLI",
	Version: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.GitCommit, version.BuildTime),
	Long:    rootLong,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		level, err := logrus.ParseLevel(logLevel)
		if err != nil {
			return err
		}

		log.SetLevel(level)
		log.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})

		if jsonFlag, _ := cmd.Flags().GetBool("json"); jsonFlag {
			outputFormat = "json"
		}

		if shouldCheckForUpdate(cmd) {
			go backgroundUpdateCheck()
		}

		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
		if !shouldCheckForUpdate(cmd) {
			return nil
		}

		printUpdateNotification()

		return nil
	},
	SilenceUsage: true,
	// Errors are printed by Execute below, which knows which failures were
	// already fully reported on stdout (e.g. query-raw's JSON error body).
	SilenceErrors: true,
}

// Execute runs the root command and translates its error into a process
// exit code.
func Execute() {
	err := executeWithSignals()
	if err == nil {
		return
	}

	var exitErr *exitCodeError
	isExitErr := errors.As(err, &exitErr)

	if !isExitErr || !exitErr.reported {
		fmt.Fprintln(os.Stderr, rootCmd.ErrPrefix(), err.Error())
	}

	if hint := unknownCommandHint(err); hint != "" {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, hint)
	}

	if isExitErr {
		os.Exit(exitErr.code)
	}

	os.Exit(1)
}

// executeWithSignals runs the root command with a context that is canceled
// on SIGINT or SIGTERM, so in-flight work aborts when the user hits Ctrl+C.
func executeWithSignals() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	attachUsageToArgErrors(rootCmd)

	return rootCmd.ExecuteContext(ctx)
}

// attachUsageToArgErrors wraps every command's positional-arg validator so a
// count/shape error carries the command's usage line. SilenceUsage hides the
// usage block on runtime errors, which is right for HTTP failures but leaves
// "accepts 1 arg(s), received 0" with no path to the correct invocation.
func attachUsageToArgErrors(cmd *cobra.Command) {
	if validate := cmd.Args; validate != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := validate(c, args); err != nil {
				return fmt.Errorf("%w\n\nUsage: %s", err, c.UseLine())
			}

			return nil
		}
	}

	for _, sub := range cmd.Commands() {
		attachUsageToArgErrors(sub)
	}
}

func unknownCommandHint(err error) string {
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		return ""
	}

	return `Tip: panda has fixed workflow, discovery, and datasource commands; most topic words are search terms, not commands. Run 'panda getting-started' for the workflow.`
}

func init() {
	rootCmd.SetVersionTemplate("panda version {{.Version}}\n")

	// SilenceUsage suppresses the usage block everywhere, so a flag typo
	// would otherwise surface as a bare "unknown flag" with no correction
	// path. Attach the one-line usage to the error itself.
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf(
			"%w\n\nUsage: %s\nRun '%s --help' for available flags",
			err, cmd.UseLine(), cmd.CommandPath(),
		)
	})

	rootCmd.AddGroup(
		&cobra.Group{ID: groupWorkflow, Title: "Workflow:"},
		&cobra.Group{ID: groupDiscovery, Title: "Discovery:"},
		&cobra.Group{ID: groupDirect, Title: "Direct Access:"},
		&cobra.Group{ID: groupSetup, Title: "Setup:"},
	)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "",
		"config file (default: $PANDA_CONFIG, ~/.config/panda/config.yaml, or ./config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info",
		"log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text",
		"output format (text, json)")
	rootCmd.PersistentFlags().Bool("json", false,
		"output in JSON format (shorthand for --output json)")
	_ = rootCmd.PersistentFlags().MarkHidden("json")

	_ = rootCmd.RegisterFlagCompletionFunc("log-level", cobra.FixedCompletions(
		[]string{"debug", "info", "warn", "error"}, cobra.ShellCompDirectiveNoFileComp,
	))
	_ = rootCmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions(
		[]string{"text", "json"}, cobra.ShellCompDirectiveNoFileComp,
	))
	_ = rootCmd.RegisterFlagCompletionFunc("config",
		func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{"yaml", "yml"}, cobra.ShellCompDirectiveFilterFileExt
		})
}

// shouldCheckForUpdate reports whether a command run should trigger the
// background update check and notification. Dev builds have no release
// to compare against, so they never check.
func shouldCheckForUpdate(cmd *cobra.Command) bool {
	return version.Version != "dev" && !skipUpdateCheckCommands[cmd.Name()]
}

// backgroundUpdateCheck returns a cached version if fresh, otherwise
// queries GitHub and updates the cache.
func backgroundUpdateCheck() {
	// Use cached result if it's less than 10 minutes old.
	if cache, _ := github.LoadCache(); cache != nil && cache.IsFresh() {
		updateResult <- &cache.LatestVersion
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	checker := github.NewReleaseChecker(github.RepoOwner, github.RepoName)

	release, err := checker.LatestRelease(ctx)
	if err != nil {
		updateResult <- nil
		return
	}

	_ = github.SaveCache(&github.UpdateCache{
		LatestVersion: release.TagName,
		CheckedAt:     time.Now(),
	})

	updateResult <- &release.TagName
}

// printUpdateNotification waits briefly for the background check and
// prints a one-line update notice to stderr if a newer version exists.
func printUpdateNotification() {
	var latestVersion *string

	select {
	case latestVersion = <-updateResult:
	case <-time.After(2 * time.Second):
		return
	}

	if latestVersion == nil {
		return
	}

	if version.IsNewer(version.Version, *latestVersion) {
		fmt.Fprintf(os.Stderr,
			"\nUpdate available: %s -> %s. Run 'panda upgrade' to update.\n",
			version.Version, *latestVersion,
		)
	}
}
