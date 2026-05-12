package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractQualifiedTableName(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		wantDB   string
		wantName string
	}{
		{name: "qualified", uri: "clickhouse://tables/logs_internal/logs", wantDB: "logs_internal", wantName: "logs"},
		{name: "qualified xatu-cbt", uri: "clickhouse://tables/mainnet/fct_block_head", wantDB: "mainnet", wantName: "fct_block_head"},
		{name: "qualified short db name", uri: "clickhouse://tables/internal/logs", wantDB: "internal", wantName: "logs"},
		{name: "bare invalid", uri: "clickhouse://tables/logs", wantDB: "", wantName: ""},
		{name: "trailing slash invalid", uri: "clickhouse://tables/db/", wantDB: "", wantName: ""},
		{name: "leading slash invalid", uri: "clickhouse://tables//table", wantDB: "", wantName: ""},
		{name: "three segments invalid", uri: "clickhouse://tables/a/b/c", wantDB: "", wantName: ""},
		{name: "missing prefix", uri: "clickhouse://something/db/table", wantDB: "", wantName: ""},
		{name: "empty", uri: "", wantDB: "", wantName: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDB, gotName := extractQualifiedTableName(tt.uri)

			assert.Equal(t, tt.wantDB, gotDB)
			assert.Equal(t, tt.wantName, gotName)
		})
	}
}

func TestTableKey(t *testing.T) {
	assert.Equal(t, "logs_internal.logs", tableKey("logs_internal", "logs"))
	assert.Equal(t, "default.events", tableKey("default", "events"))
}
