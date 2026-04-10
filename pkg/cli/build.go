package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

const (
	buildPollInterval = 15 * time.Second
)

var (
	buildRef       string
	buildRepo      string
	buildDockerTag string
	buildNoWait    bool
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

By default the command waits for the build to complete. Use --no-wait
to trigger and return immediately.

Examples:
  panda build geth                        # build and wait
  panda build geth --ref master
  panda build lighthouse --ref unstable
  panda build geth --no-wait              # fire and forget
  panda build geth --repo ethereum/go-ethereum --ref my-branch
  panda build geth --ref my-branch --tag my-custom-tag`,
	Args: cobra.ExactArgs(1),
	RunE: runBuild,
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringVar(&buildRef, "ref", "", "branch, tag, or SHA to build from (uses workflow default if omitted)")
	buildCmd.Flags().StringVar(&buildRepo, "repo", "", "source repository override (e.g. user/go-ethereum)")
	buildCmd.Flags().StringVar(&buildDockerTag, "tag", "", "override target docker tag")
	buildCmd.Flags().BoolVar(&buildNoWait, "no-wait", false, "trigger the build and return immediately without waiting")
}

func runBuild(_ *cobra.Command, args []string) error {
	client := args[0]
	ctx := context.Background()

	resp, err := triggerBuild(ctx, serverapi.BuildTriggerRequest{
		Client:     client,
		Repository: buildRepo,
		Ref:        buildRef,
		DockerTag:  buildDockerTag,
	})
	if err != nil {
		return fmt.Errorf("triggering build: %w", err)
	}

	if buildNoWait {
		return printBuildTriggered(resp)
	}

	// Default: wait for completion.
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

	return nil
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
