package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCreateTable_TableComment(t *testing.T) {
	tests := []struct {
		name            string
		createStmt      string
		expectedComment string
		expectedEngine  string
		expectedCols    int
	}{
		{
			name: "table comment not confused with column comment",
			createStmt: "CREATE TABLE default.test_table\n" +
				"(\n" +
				"    `updated_date_time` DateTime COMMENT 'Timestamp when the record was last updated',\n" +
				"    `name` String COMMENT 'The name of the thing'\n" +
				")\n" +
				"ENGINE = MergeTree\n" +
				"ORDER BY updated_date_time\n" +
				"COMMENT 'Aggregate and proof gossipsub messages'",
			expectedComment: "Aggregate and proof gossipsub messages",
			expectedEngine:  "MergeTree",
			expectedCols:    2,
		},
		{
			name: "no table comment with column comments",
			createStmt: "CREATE TABLE default.test_table\n" +
				"(\n" +
				"    `id` UInt64 COMMENT 'Primary key'\n" +
				")\n" +
				"ENGINE = MergeTree\n" +
				"ORDER BY id",
			expectedComment: "",
			expectedEngine:  "MergeTree",
			expectedCols:    1,
		},
		{
			name: "table comment without column comments",
			createStmt: "CREATE TABLE default.test_table\n" +
				"(\n" +
				"    `id` UInt64,\n" +
				"    `name` String\n" +
				")\n" +
				"ENGINE = ReplacingMergeTree\n" +
				"COMMENT 'Simple table description'",
			expectedComment: "Simple table description",
			expectedEngine:  "ReplacingMergeTree",
			expectedCols:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, err := parseCreateTable("test_table", tt.createStmt)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedComment, schema.Comment)
			assert.Equal(t, tt.expectedEngine, schema.Engine)
			assert.Len(t, schema.Columns, tt.expectedCols)
		})
	}
}

func TestParseCreateTable_KeyClauses(t *testing.T) {
	createStmt := "CREATE TABLE default.test_table\n" +
		"(\n" +
		"    `ts` DateTime,\n" +
		"    `id` UInt64,\n" +
		"    `name` String\n" +
		")\n" +
		"ENGINE = MergeTree\n" +
		"PARTITION BY toYYYYMM(ts)\n" +
		"ORDER BY (ts, id)\n" +
		"PRIMARY KEY ts\n" +
		"SETTINGS index_granularity = 8192"

	schema, err := parseCreateTable("test_table", createStmt)
	require.NoError(t, err)

	assert.Equal(t, "toYYYYMM(ts)", schema.PartitionBy)
	assert.Equal(t, "(ts, id)", schema.OrderBy)
	assert.Equal(t, "ts", schema.PrimaryKey)
}

func TestExtractCreateClause_IgnoresNestedKeywords(t *testing.T) {
	suffix := "ENGINE = MergeTree\n" +
		"ORDER BY (if(kind = 'PRIMARY KEY', a, b), c)\n" +
		"COMMENT 'ORDER BY text in a comment'"

	assert.Equal(t, "(if(kind = 'PRIMARY KEY', a, b), c)", extractCreateClause(suffix, "ORDER BY"))
}

func TestValidateDatabaseIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "public database", input: "default", wantErr: false},
		{name: "underscored database", input: "gas_analysis", wantErr: false},
		{name: "hyphenated devnet database", input: "glamsterdam-devnet-6", wantErr: false},
		{name: "mixed case", input: "INFORMATION_SCHEMA", wantErr: false},
		{name: "leading digit", input: "6-devnet", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "backtick breakout", input: "db`.`system.tables", wantErr: true},
		{name: "space", input: "my db", wantErr: true},
		{name: "semicolon injection", input: "db;DROP", wantErr: true},
		{name: "dot qualified", input: "a.b", wantErr: true},
		{name: "quote", input: "db'", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabaseIdentifier(tt.input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
