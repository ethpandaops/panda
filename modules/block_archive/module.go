package blockarchive

import (
	"context"
	"fmt"
	"net/url"

	"gopkg.in/yaml.v3"

	"github.com/ethpandaops/panda/pkg/types"
)

// Module implements the module.Module interface for the Block Archive module.
type Module struct {
	cfg Config
}

// New creates a new Block Archive module.
func New() *Module {
	return &Module{}
}

func (p *Module) Name() string { return "block_archive" }

// Enabled reports whether Block Archive operations should be exposed.
func (p *Module) Enabled() bool { return p.cfg.IsEnabled() }

// DefaultEnabled implements module.DefaultEnabled.
// Block Archive is enabled by default since it requires no configuration.
func (p *Module) DefaultEnabled() bool { return true }

func (p *Module) Init(rawConfig []byte) error {
	if len(rawConfig) == 0 {
		return nil
	}

	return yaml.Unmarshal(rawConfig, &p.cfg)
}

func (p *Module) ApplyDefaults() {}

func (p *Module) Validate() error {
	if !p.cfg.IsEnabled() {
		return nil
	}

	raw := p.cfg.BaseURL()

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid block_archive url %q: %w", raw, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("block_archive url scheme must be http or https, got %q", parsed.Scheme)
	}

	if parsed.Host == "" {
		return fmt.Errorf("block_archive url %q has no host", raw)
	}

	if parsed.User != nil {
		return fmt.Errorf("block_archive url %q must not contain userinfo", raw)
	}

	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("block_archive url %q must not contain a query or fragment", raw)
	}

	return nil
}

// URL returns the configured block-archiver base URL.
func (p *Module) URL() string {
	return p.cfg.BaseURL()
}

// SandboxEnv returns environment variables for the sandbox.
// Returns ETHPANDAOPS_BLOCK_ARCHIVE_URL so the Python wrapper knows the
// module is enabled. Network discovery happens server-side at call time.
func (p *Module) SandboxEnv() (map[string]string, error) {
	if !p.cfg.IsEnabled() {
		return nil, nil
	}

	return map[string]string{
		"ETHPANDAOPS_BLOCK_ARCHIVE_URL": p.cfg.BaseURL(),
	}, nil
}

// DatasourceInfo returns empty since the block archive is a single hosted
// service, not a per-network datasource.
func (p *Module) DatasourceInfo() []types.DatasourceInfo {
	return nil
}

func (p *Module) Examples() map[string]types.ExampleCategory {
	if !p.cfg.IsEnabled() {
		return nil
	}

	result := make(map[string]types.ExampleCategory, len(queryExamples))
	for k, v := range queryExamples {
		result[k] = v
	}

	return result
}

func (p *Module) PythonAPIDocs() map[string]types.ModuleDoc {
	if !p.cfg.IsEnabled() {
		return nil
	}

	return map[string]types.ModuleDoc{
		"block_archive": {
			Description: "Fetch raw beacon blocks (SSZ or decoded JSON) by (network, slot, block_root) from the public block archive.",
			Functions: map[string]types.FunctionDoc{
				"list_networks":  {Signature: "list_networks(active_only=True) -> list[dict]", Description: "List networks the archive knows about. Each entry: {name, status, source, tracoor_url, chain_id, polling}. Defaults to active+polling networks; pass active_only=False to include inactive devnets that still have historical blocks."},
				"get_base_url":   {Signature: "get_base_url() -> str", Description: "Get the block-archiver base URL"},
				"download_ssz":   {Signature: "download_ssz(network, slot, block_root) -> bytes", Description: "Download the SSZ-encoded SignedBeaconBlock bytes"},
				"get_block_json": {Signature: "get_block_json(network, slot, block_root) -> dict", Description: "Get the decoded JSON representation of the SignedBeaconBlock. Raises ValueError (HTTP 501 no_fork_schedule) for networks without an embedded fork schedule (most devnets) — use download_ssz instead for those."},
				"link":           {Signature: "link(network, slot, block_root) -> str", Description: "Build a browser link to the block's UI page in the archive"},
			},
		},
	}
}

func (p *Module) GettingStartedSnippet() string {
	if !p.cfg.IsEnabled() {
		return ""
	}

	return `## Block Archive

Fetch raw canonical beacon blocks by (network, slot, block_root). Source the
(slot, block_root) pairs from clickhouse, then pull the raw payload from the
archive when you need the SSZ or decoded JSON.

The archive serves mainnet, sepolia, hoodi, plus a rotating set of active
devnets discovered from cartographoor. Inactive devnets keep their archived
history available — pass active_only=False to ` + "`list_networks`" + ` to see them.

The decoded-JSON endpoint needs a known fork schedule, which only exists for
mainnet/sepolia/hoodi. For devnets use ` + "`download_ssz`" + ` and decode locally.

` + "```python" + `
from ethpandaops import block_archive, clickhouse

# What's the archive currently polling?
for n in block_archive.list_networks():
    print(n["name"], n["status"], "polling" if n["polling"] else "")

# Look up a recent block_root for a slot from xatu.
df = clickhouse.query("xatu", """
    SELECT slot, block_root
    FROM beacon_api_eth_v1_events_block
    WHERE meta_network_name = 'mainnet'
    ORDER BY slot DESC
    LIMIT 1
""")
slot = int(df.iloc[0]["slot"])
root = df.iloc[0]["block_root"]

# Decoded JSON (mainnet/sepolia/hoodi only).
block = block_archive.get_block_json("mainnet", slot, root)
print(block["block"]["message"]["proposer_index"])

# Raw SSZ bytes — works for every archived network including devnets.
raw = block_archive.download_ssz("mainnet", slot, root)
` + "```" + `
`
}

func (p *Module) Start(_ context.Context) error { return nil }

func (p *Module) Stop(_ context.Context) error { return nil }
