package cli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/sha3"
)

func newEVMWalletCommand() *cobra.Command {
	var outputFile string
	cmd := &cobra.Command{
		Use: "wallet", Short: "Generate a wallet locally and save its key in a private file", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			address, err := createEVMWallet(outputFile)
			if err != nil {
				return err
			}
			return printJSON(map[string]string{"address": address, "wallet_file": outputFile})
		},
	}
	cmd.Flags().StringVar(&outputFile, "output-file", "", "New local wallet JSON file (mode 0600; existing files are never overwritten)")
	_ = cmd.MarkFlagRequired("output-file")
	return cmd
}

// evmAddress follows Ethereum's Keccak-256 public key derivation. Addresses
// are lowercase hex; signing libraries may render the same address checksummed.
func evmAddress(key *secp256k1.PrivateKey) string {
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(key.PubKey().SerializeUncompressed()[1:])
	digest := hash.Sum(nil)
	return "0x" + hex.EncodeToString(digest[12:])
}

// createEVMWallet keeps the key on the CLI host, out of server execution output.
func createEVMWallet(path string) (string, error) {
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return "", fmt.Errorf("generating wallet: %w", err)
	}
	defer key.Zero()
	address := evmAddress(key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating wallet file: %w", err)
	}
	writeErr := json.NewEncoder(f).Encode(map[string]string{"address": address, "private_key": "0x" + hex.EncodeToString(key.Serialize())})
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return "", fmt.Errorf("saving wallet: %w", errors.Join(writeErr, closeErr, os.Remove(path)))
	}
	return address, nil
}
