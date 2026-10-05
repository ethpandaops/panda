package cli

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	evm := &cobra.Command{GroupID: groupDirect, Use: "evm", Short: "Fund saved wallets with recoverable faucet jobs"}
	rootCmd.AddCommand(evm)
	var amount, requestID string
	var noWait bool
	var waitTimeout time.Duration
	claim := &cobra.Command{
		Use: "faucet <network> <address>", Short: "Start or recover a claim; wait for a successful receipt",
		Long: `Fund a previously saved wallet through a bounded server-owned job.
Discover network IDs with 'panda ethnode networks'. The faucet needs the
address only. Save the wallet before funding. Interrupting this CLI wait
does not cancel the job; recover with 'panda evm faucet-status <job-id>'.
Repeating the same network/address/amount recovers the existing claim for
one hour after completion. Use a new --request-id only for an intentional
additional claim after checking the original address balance and receipt.
Jobs do not survive a local server restart. --no-wait returns immediately.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !noWait && waitTimeout <= 0 {
				return fmt.Errorf("--wait-timeout must be positive")
			}
			operationArgs := map[string]any{"network": args[0], "address": args[1]}
			if amount != "" {
				for _, digit := range amount {
					if digit < '0' || digit > '9' {
						return fmt.Errorf("--amount-wei must be a positive decimal integer")
					}
				}
				value, ok := new(big.Int).SetString(amount, 10)
				if !ok || value.Sign() <= 0 {
					return fmt.Errorf("--amount-wei must be a positive decimal integer")
				}
				operationArgs["amount_wei"] = value.String()
			}
			if requestID != "" {
				operationArgs["request_id"] = requestID
			}
			response, err := runServerOperation(cmd, "evm.faucet_start", operationArgs)
			if err != nil {
				return err
			}
			job, ok := response.Data.(map[string]any)
			if !ok {
				return fmt.Errorf("unexpected faucet job response")
			}
			if noWait {
				return printJSON(job)
			}
			id, ok := job["job_id"].(string)
			if !ok || id == "" {
				return fmt.Errorf("faucet returned no job_id")
			}
			fmt.Fprintf(os.Stderr, "Faucet job: %s; recover with panda evm faucet-status %s --wait\n", id, id)
			return waitFaucetJob(cmd.Context(), id, waitTimeout)
		},
	}
	claim.Flags().StringVar(&amount, "amount-wei", "", "Exact payout as decimal wei (default: faucet minimum)")
	claim.Flags().StringVar(&requestID, "request-id", "", "Stable recovery key; a new key intentionally starts an additional claim")
	claim.Flags().BoolVar(&noWait, "no-wait", false, "Return the job immediately; poll with faucet-status")
	claim.Flags().DurationVar(&waitTimeout, "wait-timeout", 13*time.Minute, "CLI wait budget; timeout leaves the server job running")
	claim.ValidArgsFunction = completeEthNodeArgs
	evm.AddCommand(claim)
	var wait bool
	var statusWaitTimeout time.Duration
	status := &cobra.Command{
		Use: "faucet-status <job-id>", Short: "Read claim progress or wait for receipt confirmation", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait {
				return waitFaucetJob(cmd.Context(), args[0], statusWaitTimeout)
			}
			job, err := fetchFaucetJob(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printJSON(job)
		},
	}
	status.Flags().BoolVar(&wait, "wait", false, "Wait for a successful on-chain receipt")
	status.Flags().DurationVar(&statusWaitTimeout, "wait-timeout", 13*time.Minute, "CLI wait budget")
	evm.AddCommand(status)
}

func fetchFaucetJob(ctx context.Context, id string) (map[string]any, error) {
	response, err := serverOperation(ctx, "evm.faucet_status", map[string]any{"job_id": id})
	if err != nil {
		return nil, err
	}
	job, ok := response.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected faucet job response")
	}
	return job, nil
}

func waitFaucetJob(parent context.Context, id string, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("wait timeout must be positive; job %s remains recoverable", id)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		job, err := fetchFaucetJob(ctx, id)
		if err != nil {
			return fmt.Errorf("faucet wait failed; recover job %s with faucet-status: %w", id, err)
		}
		if job["state"] == "confirmed" {
			return printJSON(job)
		}
		if job["state"] == "failed" || job["terminal"] == true {
			if err := printJSON(job); err != nil {
				return err
			}
			return fmt.Errorf("job %s ended without confirmation: %v; check the original balance/receipt before retrying", id, job["error"])
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("faucet wait ended; recover job %s with faucet-status: %w", id, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}
