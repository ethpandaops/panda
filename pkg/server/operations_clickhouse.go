package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

func (s *service) handleClickHouseOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "clickhouse.list_datasources":
		s.handleClickHouseListDatasources(w)
	case "clickhouse.query", "clickhouse.query_raw":
		s.handleClickHouseQuery(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleClickHouseListDatasources(w http.ResponseWriter) {
	items := make([]listItem, 0)
	for _, info := range s.proxyService.ClickHouseDatasourceInfo() {
		item := listItem{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
			Type:        info.Type,
		}
		if database := info.Metadata["database"]; database != "" {
			item.Extra = map[string]any{"database": database}
		}
		items = append(items, item)
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"datasources": items},
	})
}

func (s *service) handleClickHouseQuery(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, err := requiredStringArg(req.Args, "datasource")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	sql, err := requiredStringArg(req.Args, "sql")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := url.Values{"default_format": {"TabSeparatedWithNames"}}
	for key, value := range optionalMapArg(req.Args, "parameters") {
		params.Set("param_"+key, formatClickHouseParamValue(value))
	}

	body, status, headers, err := s.proxyDatasourceRequest(
		r.Context(),
		"clickhouse",
		datasource,
		http.MethodPost,
		"/clickhouse/?"+params.Encode(),
		strings.NewReader(sql),
		http.Header{
			handlers.DatasourceHeader: []string{datasource},
			"Content-Type":            []string{"text/plain"},
		},
	)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	if status < 200 || status >= 300 {
		writeAPIError(w, status, clickHouseErrorWithHint(strings.TrimSpace(string(body))))
		return
	}

	writePassthroughResponse(w, http.StatusOK, headers.Get("Content-Type"), body)
}

func clickHouseErrorWithHint(message string) string {
	if message == "" || strings.Contains(strings.ToLower(message), "\nhint:") {
		return message
	}

	hint := clickHouseErrorHint(message)
	if hint == "" {
		return message
	}

	return message + "\n\nhint: " + hint
}

func clickHouseErrorHint(message string) string {
	switch {
	case strings.Contains(message, "INDEX_NOT_USED") || strings.Contains(message, "force_primary_key"):
		return "ClickHouse requires the query to use the table's primary/partition key. Add a bounded time filter such as slot_start_date_time >= now() - INTERVAL 1 HOUR on slot-based tables. For latest-value questions, group within that recent window and ORDER BY slot DESC LIMIT 1 instead of using an unbounded max(slot) subquery."
	case strings.Contains(message, "DISTRIBUTED_IN_JOIN_SUBQUERY_DENIED"):
		return "ClickHouse denied a join or IN against distributed subqueries. Prefer one grouped query over a recent partition window, split the lookup into two queries, or use GLOBAL JOIN/IN when a distributed join is really required."
	case strings.Contains(message, "UNKNOWN_IDENTIFIER"):
		return "ClickHouse does not recognize one of the selected columns, aliases, or functions. Inspect the table with panda schema <cluster> <database> <table> or DESCRIBE TABLE before retrying."
	case strings.Contains(message, "SYNTAX_ERROR") && strings.Contains(message, "toDateTime"):
		return "String and DateTime literals must be quoted. In Python, prefer ClickHouse parameters or repr(value) when interpolating timestamps and hex strings."
	default:
		return ""
	}
}

func formatClickHouseParamValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(v)
	}
}
