package clickhouse

import "strings"

// QueryErrorClass identifies a recognizable class of ClickHouse query failure.
// Classification is integration knowledge and lives here; consumers (CLI, MCP
// surfaces) own the wording of any guidance they attach to a class.
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
	}

	return QueryErrorUnknown
}
