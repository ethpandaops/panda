package clickhouse

import (
	"fmt"
	"regexp"
	"strings"
)

// Pre-compiled regexes for schema parsing.
var (
	// validIdentifier matches valid ClickHouse table/column identifiers.
	validIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// enginePattern extracts the engine name from a CREATE TABLE statement.
	enginePattern = regexp.MustCompile(`ENGINE\s*=\s*(\w+)`)

	// tableCommentPattern extracts the table comment from a CREATE TABLE statement.
	tableCommentPattern = regexp.MustCompile(`COMMENT\s+'([^']*)'`)

	// columnPattern extracts column name and type from a column definition line.
	columnPattern = regexp.MustCompile("(?m)^\\s*`([^`]+)`\\s+([^,\\n]+)")

	// columnCommentPattern extracts the comment from a column definition.
	columnCommentPattern = regexp.MustCompile(`COMMENT\s+'([^']*)'`)

	// defaultPattern extracts the default expression from a column definition.
	defaultPattern = regexp.MustCompile(`(DEFAULT|MATERIALIZED|ALIAS)\s+([^,\n]+?)(?:\s+(?:CODEC|COMMENT|$))`)
)

type clickhouseDDLParser struct{}

func validateIdentifier(name string) error {
	if !validIdentifier.MatchString(name) {
		return fmt.Errorf("invalid identifier %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
	}

	return nil
}

func parseCreateTable(tableName, createStmt string) (*TableSchema, error) {
	return (clickhouseDDLParser{}).ParseTable(tableName, createStmt)
}

func (clickhouseDDLParser) ParseTable(tableName, createStmt string) (*TableSchema, error) {
	schema := &TableSchema{
		Name:            tableName,
		CreateStatement: createStmt,
		Columns:         make([]TableColumn, 0, 32),
	}

	if matches := enginePattern.FindStringSubmatch(createStmt); len(matches) > 1 {
		schema.Engine = matches[1]
	}

	if matches := tableCommentPattern.FindStringSubmatch(createStmt); len(matches) > 1 {
		schema.Comment = matches[1]
	}

	startIdx := strings.Index(createStmt, "(")
	if startIdx == -1 {
		return schema, nil
	}

	depth := 0
	endIdx := -1

outerLoop:
	for i := startIdx; i < len(createStmt); i++ {
		switch createStmt[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				endIdx = i

				break outerLoop
			}
		}
	}

	if endIdx == -1 {
		return schema, nil
	}

	columnsSection := createStmt[startIdx+1 : endIdx]
	lines := strings.Split(columnsSection, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "INDEX") || strings.HasPrefix(line, "PROJECTION") {
			continue
		}

		colMatches := columnPattern.FindStringSubmatch(line)
		if len(colMatches) < 3 {
			continue
		}

		col := TableColumn{
			Name: colMatches[1],
			Type: cleanColumnType(strings.TrimSpace(colMatches[2])),
		}

		if commentMatches := columnCommentPattern.FindStringSubmatch(line); len(commentMatches) > 1 {
			col.Comment = commentMatches[1]
		}

		if defaultMatches := defaultPattern.FindStringSubmatch(line); len(defaultMatches) > 2 {
			col.DefaultType = defaultMatches[1]
			col.DefaultValue = strings.TrimSpace(defaultMatches[2])
		}

		if col.Name == "meta_network_name" {
			schema.HasNetworkCol = true
		}

		schema.Columns = append(schema.Columns, col)
	}

	return schema, nil
}

func cleanColumnType(colType string) string {
	for _, keyword := range []string{" DEFAULT", " CODEC", " COMMENT", " MATERIALIZED", " ALIAS"} {
		if idx := strings.Index(strings.ToUpper(colType), keyword); idx != -1 {
			colType = colType[:idx]
		}
	}

	return strings.TrimSuffix(strings.TrimSpace(colType), ",")
}
