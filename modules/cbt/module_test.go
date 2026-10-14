package cbt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNetworkBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		network string
		want    string
	}{
		{
			name:    "named network uses vanity host",
			network: "mainnet",
			want:    "https://cbt.mainnet.ethpandaops.io",
		},
		{
			name:    "hoodi uses vanity host",
			network: "hoodi",
			want:    "https://cbt.hoodi.ethpandaops.io",
		},
		{
			name:    "glamsterdam devnet uses analytics ingress host",
			network: "glamsterdam-devnet-7",
			want:    "https://glamsterdam-devnet-7-xatu-cbt.analytics.production.platform.ethpandaops.io",
		},
		{
			name:    "bal devnet uses analytics ingress host",
			network: "bal-devnet-7",
			want:    "https://bal-devnet-7-xatu-cbt.analytics.production.platform.ethpandaops.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NetworkBaseURL(tt.network))
		})
	}
}
