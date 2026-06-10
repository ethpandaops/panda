package datasets

import (
	"regexp"
	"strings"
)

var (
	// tableRefPattern captures the identifier following FROM/JOIN. It matches
	// bare names (canonical_beacon_validators), db-qualified names
	// (external.otel_logs) and templated names ({network}.fct_block_head). It
	// does not match subqueries because "FROM (" does not start with an
	// identifier character.
	tableRefPattern = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([A-Za-z_{][A-Za-z0-9_{}.]*)`)

	// cteNamePattern captures common-table-expression names declared via
	// "WITH name AS (" or ", name AS (" so they are not mistaken for tables.
	cteNamePattern = regexp.MustCompile(`(?i)(?:WITH\s+|,\s*)([A-Za-z_][A-Za-z0-9_]*)\s+AS\s*\(`)

	// arrayJoinPattern matches ClickHouse's ARRAY JOIN clause, which is followed
	// by column expressions, not a table. It is removed before table extraction
	// so its columns are not mistaken for tables.
	arrayJoinPattern = regexp.MustCompile(`(?i)\bARRAY\s+JOIN\b`)
)

// extractTableRefs returns the bare table names referenced by FROM/JOIN clauses
// in a SQL query, excluding CTE names and subqueries. Database and {network}
// prefixes are stripped so refs can be matched against a set of bare table
// names. The result is deduplicated.
func extractTableRefs(sql string) []string {
	// Strip ARRAY JOIN before matching: it is followed by columns, and leaving
	// it in place would let the JOIN keyword capture a column as a table.
	sql = arrayJoinPattern.ReplaceAllString(sql, " ")

	ctes := make(map[string]bool)
	for _, m := range cteNamePattern.FindAllStringSubmatch(sql, -1) {
		ctes[strings.ToLower(m[1])] = true
	}

	seen := make(map[string]bool)

	var refs []string

	for _, m := range tableRefPattern.FindAllStringSubmatch(sql, -1) {
		name := bareTableName(m[1])
		if name == "" {
			continue
		}

		if ctes[strings.ToLower(name)] {
			continue
		}

		if seen[name] {
			continue
		}

		seen[name] = true

		refs = append(refs, name)
	}

	return refs
}

// bareTableName strips database / {network} prefixes and template braces,
// returning the final path segment (the table name).
func bareTableName(ref string) string {
	ref = strings.TrimSpace(ref)
	if idx := strings.LastIndex(ref, "."); idx >= 0 {
		ref = ref[idx+1:]
	}

	ref = strings.NewReplacer("{", "", "}", "").Replace(ref)

	return strings.TrimSpace(ref)
}

// unknownTableRefs returns the tables referenced by the query that are absent
// from the known set. A query with no extractable table references yields none
// (conservative — never demote what we cannot parse).
func unknownTableRefs(sql string, known map[string]bool) []string {
	var missing []string

	for _, ref := range extractTableRefs(sql) {
		if !known[ref] {
			missing = append(missing, ref)
		}
	}

	return missing
}
