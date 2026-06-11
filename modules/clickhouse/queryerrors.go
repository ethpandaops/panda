package clickhouse

import "strings"

// QueryErrorClass identifies a recognizable class of ClickHouse query failure.
// Classification is integration knowledge and lives here; consumers (CLI, MCP
// surfaces) own the wording of any guidance they attach to a class. The sandbox
// Python library keeps a mirrored matcher in python/clickhouse.py (it cannot
// call into Go) — keep the two in sync when classes change.
type QueryErrorClass int

const (
	// QueryErrorUnknown is any error this module does not recognize.
	QueryErrorUnknown QueryErrorClass = iota
	// QueryErrorPrimaryKeyFilterRequired: the cluster enforces
	// force_primary_key / index-usage and the query had no selective filter.
	QueryErrorPrimaryKeyFilterRequired
	// QueryErrorUnknownIdentifier: the SQL references a column or expression
	// the selected table does not have.
	QueryErrorUnknownIdentifier
	// QueryErrorNotAggregate: a selected expression is neither aggregated nor
	// in GROUP BY.
	QueryErrorNotAggregate
	// QueryErrorSyntax: ClickHouse rejected the SQL syntax.
	QueryErrorSyntax
	// QueryErrorDatasourceNotFound: the referenced ClickHouse datasource does
	// not exist.
	QueryErrorDatasourceNotFound
	// QueryErrorDistributedJoinDenied: ClickHouse rejected a distributed
	// subquery or join under the datasource's distributed_product_mode.
	QueryErrorDistributedJoinDenied
	// QueryErrorUnknownTable: the SQL references a table or database that is
	// not available in the selected datasource.
	QueryErrorUnknownTable
	// QueryErrorUnknownFunction: the SQL uses a function unavailable in the
	// selected ClickHouse deployment/version.
	QueryErrorUnknownFunction
	// QueryErrorBadFunctionArguments: the SQL calls a function with an
	// incompatible column type or argument shape.
	QueryErrorBadFunctionArguments
	// QueryErrorIllegalAggregation: the SQL nests aggregate functions or uses
	// aggregate outputs in a way ClickHouse rejects.
	QueryErrorIllegalAggregation
	// QueryErrorAliasRequired: the SQL uses a subquery/table expression in a
	// join context where ClickHouse requires an explicit alias.
	QueryErrorAliasRequired
)

// ClassifyQueryError maps an upstream error message to a QueryErrorClass.
func ClassifyQueryError(message string) QueryErrorClass {
	normalized := strings.ToLower(message)

	switch {
	case strings.Contains(normalized, "index_not_used") ||
		strings.Contains(normalized, "force_primary_key") ||
		strings.Contains(normalized, "primary key") && strings.Contains(normalized, "not used"):
		return QueryErrorPrimaryKeyFilterRequired
	case strings.Contains(normalized, "unknown_identifier") ||
		strings.Contains(normalized, "unknown expression identifier") ||
		strings.Contains(normalized, "missing columns"):
		return QueryErrorUnknownIdentifier
	case strings.Contains(normalized, "not_an_aggregate"):
		return QueryErrorNotAggregate
	case strings.Contains(normalized, "syntax_error"):
		return QueryErrorSyntax
	case strings.Contains(normalized, "clickhouse datasource") && strings.Contains(normalized, "not found"):
		return QueryErrorDatasourceNotFound
	case strings.Contains(normalized, "distributed_in_join_subquery_denied") ||
		strings.Contains(normalized, "double-distributed in/join subqueries"):
		return QueryErrorDistributedJoinDenied
	case strings.Contains(normalized, "unknown_table") ||
		strings.Contains(normalized, "unknown_database") ||
		strings.Contains(normalized, "database ") && strings.Contains(normalized, " does not exist") ||
		strings.Contains(normalized, "unknown table expression identifier"):
		return QueryErrorUnknownTable
	case strings.Contains(normalized, "unknown_function") ||
		strings.Contains(normalized, "function with name") && strings.Contains(normalized, "does not exist"):
		return QueryErrorUnknownFunction
	case strings.Contains(normalized, "bad_arguments") ||
		strings.Contains(normalized, "cannot work with") ||
		strings.Contains(normalized, "cannot_parse_quoted_string"):
		return QueryErrorBadFunctionArguments
	case strings.Contains(normalized, "illegal_aggregation") ||
		strings.Contains(normalized, "aggregate function") && strings.Contains(normalized, "inside another aggregate function"):
		return QueryErrorIllegalAggregation
	case strings.Contains(normalized, "alias_required") ||
		strings.Contains(normalized, "requires alias") ||
		strings.Contains(normalized, "no alias for subquery"):
		return QueryErrorAliasRequired
	}

	return QueryErrorUnknown
}
