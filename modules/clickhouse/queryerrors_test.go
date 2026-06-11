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
		{
			"DB::Exception: Double-distributed IN/JOIN subqueries is denied. (DISTRIBUTED_IN_JOIN_SUBQUERY_DENIED)",
			QueryErrorDistributedJoinDenied,
		},
		{"DB::Exception: Unknown table expression identifier 'example_db.example_table'. (UNKNOWN_TABLE)", QueryErrorUnknownTable},
		{"DB::Exception: Database example_db does not exist. (UNKNOWN_DATABASE)", QueryErrorUnknownTable},
		{"DB::Exception: Function with name `toLower` does not exist. (UNKNOWN_FUNCTION)", QueryErrorUnknownFunction},
		{"DB::Exception: Functions lowerUTF8 cannot work with FixedString argument. (BAD_ARGUMENTS)", QueryErrorBadFunctionArguments},
		{"DB::Exception: Cannot parse quoted string. (CANNOT_PARSE_QUOTED_STRING)", QueryErrorBadFunctionArguments},
		{"DB::Exception: Aggregate function count() is found inside another aggregate function. (ILLEGAL_AGGREGATION)", QueryErrorIllegalAggregation},
		{"DB::Exception: JOIN CROSS JOIN ... no alias for subquery. (ALIAS_REQUIRED)", QueryErrorAliasRequired},
		{"connection refused", QueryErrorUnknown},
		{"", QueryErrorUnknown},
	}

	for _, tc := range cases {
		if got := ClassifyQueryError(tc.message); got != tc.want {
			t.Errorf("ClassifyQueryError(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
}
