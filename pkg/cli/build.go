package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/configpath"
	"github.com/ethpandaops/panda/pkg/serverapi"
)

const (
	buildPollInterval = 15 * time.Second
)

var (
	buildRef       string
	buildRepo      string
	buildDockerTag string
	buildWait      bool
	buildDiscordDM string
	buildNoDiscord bool
)

var buildCmd = &cobra.Command{
	GroupID: groupWorkflow,
	Use:     "build <client>",
	Short:   "Trigger a Docker image build for an Ethereum client",
	Long: `Trigger a Docker image build via GitHub Actions for an Ethereum client
or tool. The build is dispatched through the proxy which holds the
GitHub token.

Client names map directly to workflow files in the
eth-client-docker-image-builder repository:
  geth        -> build-push-geth.yml
  lighthouse  -> build-push-lighthouse.yml
  nimbus-eth2 -> build-push-nimbus-eth2.yml

By default the command triggers the build and returns immediately.
Use --wait to block until the build completes.

Examples:
  panda build geth                        # trigger and return
  panda build geth --ref master
  panda build lighthouse --ref unstable
  panda build geth --wait                 # block until completion
  panda build geth --repo ethereum/go-ethereum --ref my-branch
  panda build geth --ref my-branch --tag my-custom-tag`,
	Args: cobra.ExactArgs(1),
	RunE: runBuild,
}

var buildStatusCmd = &cobra.Command{
	Use:   "status <run-id>",
	Short: "Check the status of a GitHub Actions build run",
	Args:  cobra.ExactArgs(1),
	RunE:  runBuildStatus,
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.AddCommand(buildStatusCmd)
	buildCmd.Flags().StringVar(&buildRef, "ref", "", "branch, tag, or SHA to build from (uses workflow default if omitted)")
	buildCmd.Flags().StringVar(&buildRepo, "repo", "", "source repository override (e.g. user/go-ethereum)")
	buildCmd.Flags().StringVar(&buildDockerTag, "tag", "", "override target docker tag")
	buildCmd.Flags().BoolVar(&buildWait, "wait", false, "block until the build completes instead of returning immediately")
	buildCmd.Flags().StringVar(&buildDiscordDM, "discord-dm", "",
		"Discord username or user ID to DM on build completion (default: notifications.discord.username in config)")
	buildCmd.Flags().BoolVar(&buildNoDiscord, "no-discord-dm", false,
		"disable Discord notification for this build, even if a default username is configured")
}

func runBuild(_ *cobra.Command, args []string) error {
	client := args[0]
	ctx := context.Background()

	discordUser := resolveDiscordTarget()

	resp, err := triggerBuild(ctx, serverapi.BuildTriggerRequest{
		Client:     client,
		Repository: buildRepo,
		Ref:        buildRef,
		DockerTag:  buildDockerTag,
		Notify:     buildNotifySpec(discordUser),
	})
	if err != nil {
		return fmt.Errorf("triggering build: %w", err)
	}

	if !buildWait {
		return printBuildTriggered(resp)
	}

	// --wait: block until completion.
	if resp.RunID == 0 {
		// Trigger succeeded but we couldn't find the run ID.
		// Fall back to non-wait output.
		fmt.Fprintf(os.Stderr, "Build triggered but run ID not available, cannot wait for completion\n")
		return printBuildTriggered(resp)
	}

	fmt.Fprintf(os.Stderr, "Build triggered for %s (run %d), waiting for completion...\n", resp.Client, resp.RunID)

	if resp.RunURL != "" {
		fmt.Fprintf(os.Stderr, "  url: %s\n", resp.RunURL)
	}

	result, err := pollBuildStatus(ctx, resp.RunID)
	if err != nil {
		return fmt.Errorf("polling build status: %w", err)
	}

	if isJSON() {
		return printJSON(result)
	}

	switch result.Conclusion {
	case "success":
		fmt.Fprintf(os.Stderr, "Build completed successfully\n")
	case "failure":
		fmt.Fprintf(os.Stderr, "Build failed\n")
		fmt.Fprintf(os.Stderr, "  url: %s\n", result.HTMLURL)

		return fmt.Errorf("build failed (run %d)", result.RunID)
	case "cancelled":
		fmt.Fprintf(os.Stderr, "Build was cancelled\n")

		return fmt.Errorf("build cancelled (run %d)", result.RunID)
	default:
		fmt.Fprintf(os.Stderr, "Build finished with conclusion: %s\n", result.Conclusion)
	}

	return nil
}

func runBuildStatus(_ *cobra.Command, args []string) error {
	runID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}

	ctx := context.Background()

	status, err := getBuildStatus(ctx, runID)
	if err != nil {
		return fmt.Errorf("checking build status: %w", err)
	}

	if isJSON() {
		return printJSON(status)
	}

	fmt.Printf("run=%d status=%s conclusion=%s url=%s\n",
		status.RunID, status.Status, status.Conclusion, status.HTMLURL)

	return nil
}

func printBuildTriggered(resp *serverapi.BuildTriggerResponse) error {
	if isJSON() {
		return printJSON(resp)
	}

	fmt.Printf("Build triggered for %s\n", resp.Client)
	fmt.Printf("  workflow:  %s\n", resp.Workflow)

	if resp.RunURL != "" {
		fmt.Printf("  url:       %s\n", resp.RunURL)
	} else {
		fmt.Printf("  url:       %s\n", resp.WorkflowURL)
	}

	if buildRef != "" {
		fmt.Printf("  ref:       %s\n", buildRef)
	}

	if buildRepo != "" {
		fmt.Printf("  repo:      %s\n", buildRepo)
	}

	if buildDockerTag != "" {
		fmt.Printf("  tag:       %s\n", buildDockerTag)
	}

	if resp.RunID > 0 {
		fmt.Println()
		fmt.Println("<assistant_hint>")
		fmt.Println("To monitor this build in the background, use the Monitor tool with the following parameters:")
		fmt.Printf("  description: \"%s build (run %d)\"\n", resp.Client, resp.RunID)
		fmt.Println("  timeout_ms: 1800000")
		fmt.Println("  persistent: false")
		fmt.Println("  command:")
		fmt.Println("    while true; do")
		fmt.Printf("      line=$(panda build status %d 2>&1) || { echo \"status_check_failed\"; sleep 15; continue; }\n", resp.RunID)
		fmt.Println("      echo \"$line\"")
		fmt.Println("      echo \"$line\" | grep -q 'status=completed' && exit 0")
		fmt.Println("      sleep 15")
		fmt.Println("    done")
		fmt.Println("</assistant_hint>")
	}

	return nil
}

// resolveDiscordTarget picks the Discord recipient for this build invocation.
// Precedence: --no-discord-dm wins; otherwise --discord-dm; otherwise the
// configured default in notifications.discord.username. When --discord-dm is
// supplied but the configured default is empty, the value is persisted to
// config.user.yaml so subsequent builds notify without re-passing the flag.
func resolveDiscordTarget() string {
	if buildNoDiscord {
		return ""
	}

	configured := loadDiscordDefault()

	if buildDiscordDM == "" {
		return configured
	}

	if configured == "" {
		if err := persistDiscordDefault(buildDiscordDM); err != nil {
			fmt.Fprintf(os.Stderr, "  (could not save Discord username to config: %v)\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  Saved %q as default for future builds (override with --discord-dm or --no-discord-dm)\n", buildDiscordDM)
		}
	}

	return buildDiscordDM
}

func buildNotifySpec(discordUser string) *serverapi.BuildNotifySpec {
	if discordUser == "" {
		return nil
	}

	return &serverapi.BuildNotifySpec{DiscordUser: discordUser}
}

func loadDiscordDefault() string {
	resolved, err := configpath.ResolveAppConfigPath(cfgFile)
	if err != nil {
		return ""
	}

	cfg, err := config.LoadWithUserOverrides(resolved)
	if err != nil {
		return ""
	}

	return cfg.Notifications.Discord.Username
}

func persistDiscordDefault(username string) error {
	resolved, err := configpath.ResolveAppConfigPath(cfgFile)
	if err != nil {
		return err
	}

	userPath := config.UserConfigPath(resolved)

	overrides, err := config.LoadUserConfigMap(userPath)
	if err != nil {
		return err
	}

	notifications, _ := overrides["notifications"].(map[string]any)
	if notifications == nil {
		notifications = make(map[string]any, 1)
	}

	discord, _ := notifications["discord"].(map[string]any)
	if discord == nil {
		discord = make(map[string]any, 1)
	}

	discord["username"] = username
	notifications["discord"] = discord
	overrides["notifications"] = notifications

	return config.SaveUserConfig(userPath, overrides)
}

func pollBuildStatus(ctx context.Context, runID int64) (*serverapi.BuildStatusResponse, error) {
	ticker := time.NewTicker(buildPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			status, err := getBuildStatus(ctx, runID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  (status check failed: %v, retrying...)\n", err)
				continue
			}

			if status.Status == "completed" {
				return status, nil
			}

			fmt.Fprintf(os.Stderr, "  status: %s\n", status.Status)
		}
	}
}
