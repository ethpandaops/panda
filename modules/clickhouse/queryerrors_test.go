package clickhouse

import "testing"

func TestClassifyQueryError(t *testing.T) {
	cases := []struct {
		message string
		want    QueryErrorClass
	}{
		{
			"Code: 277. DB::Exception: Primary key (a, b) is not used and setting 'force_primary_key' is set. (INDEX_NOT_USED)",
			QueryErrorPrimaryKeyFilterRequired,
		},
		{"DB::Exception: Unknown expression identifier 'slott'. (UNKNOWN_IDENTIFIER)", QueryErrorUnknownIdentifier},
		{"DB::Exception: Missing columns: 'foo' while processing query", QueryErrorUnknownIdentifier},
		{"DB::Exception: Column slot is not under aggregate function. (NOT_AN_AGGREGATE)", QueryErrorNotAggregate},
		{"DB::Exception: Syntax error: failed at position 12. (SYNTAX_ERROR)", QueryErrorSyntax},
		{"clickhouse datasource \"warehouse\" not found", QueryErrorDatasourceNotFound},
		{"connection refused", QueryErrorUnknown},
		{"", QueryErrorUnknown},
	}

	for _, tc := range cases {
		if got := ClassifyQueryError(tc.message); got != tc.want {
			t.Errorf("ClassifyQueryError(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
}
