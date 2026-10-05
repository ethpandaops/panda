package cli

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
)

func TestLocalWalletAddressAndRecoveryFile(t *testing.T) {
	// Known Ethereum private-key-one vector checks the address derivation.
	keyBytes := make([]byte, 32)
	keyBytes[31] = 1
	require.Equal(t, "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf", evmAddress(secp256k1.PrivKeyFromBytes(keyBytes)))
	path := filepath.Join(t.TempDir(), "wallet.json")
	address, err := createEVMWallet(path)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var wallet map[string]string
	require.NoError(t, json.Unmarshal(data, &wallet))
	privateKey, err := hex.DecodeString(wallet["private_key"][2:])
	require.NoError(t, err)
	require.Len(t, privateKey, 32)
	require.Equal(t, address, evmAddress(secp256k1.PrivKeyFromBytes(privateKey)))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = createEVMWallet(path)
	require.Error(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, after, "existing recovery wallet must never be replaced")
}
