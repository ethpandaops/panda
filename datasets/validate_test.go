package datasets

import (
	"reflect"
	"sort"
	"testing"
)

func TestExtractTableRefs(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "templated network db",
			sql:  "SELECT avg(x) FROM {network}.fct_block_first_seen_by_node FINAL WHERE slot_start_date_time >= now()",
			want: []string{"fct_block_first_seen_by_node"},
		},
		{
			name: "db-qualified otel",
			sql:  "SELECT Timestamp FROM external.otel_logs WHERE ResourceAttributes['network'] = {n:String}",
			want: []string{"otel_logs"},
		},
		{
			name: "bare raw table",
			sql:  "SELECT * FROM canonical_beacon_validators WHERE meta_network_name = 'mainnet'",
			want: []string{"canonical_beacon_validators"},
		},
		{
			name: "cte excluded, join included",
			sql: `WITH seen AS (SELECT slot FROM {network}.fct_block_head)
			      SELECT b.slot FROM {network}.fct_block_head b
			      JOIN seen s ON s.slot = b.slot
			      LEFT JOIN {network}.fct_attestation a ON a.slot = b.slot`,
			want: []string{"fct_attestation", "fct_block_head"},
		},
		{
			name: "subquery not captured as table",
			sql:  "SELECT * FROM (SELECT slot FROM {network}.fct_block) GROUP BY slot",
			want: []string{"fct_block"},
		},
		{
			name: "no from",
			sql:  "SELECT 1",
			want: nil,
		},
		{
			name: "array join columns not captured as tables",
			sql: `SELECT validator FROM canonical_beacon_elaborated_attestation
			      LEFT ARRAY JOIN validators AS validator
			      WHERE meta_network_name = 'mainnet'`,
			want: []string{"canonical_beacon_elaborated_attestation"},
		},
		{
			name: "cte after comma without space",
			sql: `WITH a AS (SELECT 1),b AS (SELECT slot FROM {network}.fct_block)
			      SELECT * FROM b JOIN a ON 1=1`,
			want: []string{"fct_block"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTableRefs(tc.sql)
			sort.Strings(got)

			want := append([]string(nil), tc.want...)
			sort.Strings(want)

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("extractTableRefs() = %v, want %v", got, want)
			}
		})
	}
}

func TestUnknownTableRefs(t *testing.T) {
	known := map[string]bool{"fct_block_head": true, "fct_attestation": true}

	if missing := unknownTableRefs("SELECT * FROM {network}.fct_block_head FINAL", known); len(missing) != 0 {
		t.Errorf("expected known table query to be valid, got missing %v", missing)
	}

	missing := unknownTableRefs("SELECT * FROM {network}.fct_block_removed", known)
	if !reflect.DeepEqual(missing, []string{"fct_block_removed"}) {
		t.Errorf("expected missing [fct_block_removed], got %v", missing)
	}

	// No extractable refs => valid (conservative).
	if missing := unknownTableRefs("SELECT 1", known); len(missing) != 0 {
		t.Errorf("expected query with no table refs to be valid, got missing %v", missing)
	}
}
