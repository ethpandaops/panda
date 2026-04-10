package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

var (
	buildRef       string
	buildRepo      string
	buildDockerTag string
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

Examples:
  panda build geth
  panda build geth --ref master
  panda build lighthouse --ref unstable
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

	if isJSON() {
		return printJSON(resp)
	}

	fmt.Printf("Build triggered for %s\n", resp.Client)
	fmt.Printf("  workflow:  %s\n", resp.Workflow)
	fmt.Printf("  url:       %s\n", resp.WorkflowURL)

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
