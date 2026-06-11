package server

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
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

		extra := make(map[string]any, 2)

		if database := info.Metadata["database"]; database != "" {
			extra["database"] = database
		}

		// Dataset bindings ride along so sandbox code can resolve dataset
		// placement (e.g. the {db} convention) without leaving Python.
		if len(info.Contents) > 0 {
			datasets := make([]map[string]any, 0, len(info.Contents))
			for _, b := range info.Contents {
				d := map[string]any{"dataset": b.Dataset}
				if len(b.Params) > 0 {
					d["params"] = b.Params
				}

				if b.Notes != "" {
					d["notes"] = b.Notes
				}

				datasets = append(datasets, d)
			}

			extra["datasets"] = datasets
		}

		if len(extra) > 0 {
			item.Extra = extra
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
		writeAPIError(w, status, strings.TrimSpace(string(body)))
		return
	}

	writePassthroughResponse(w, http.StatusOK, headers.Get("Content-Type"), body)
}

func formatClickHouseParamValue(value any) string {
	if rv := reflect.ValueOf(value); rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		return formatClickHouseArrayParamValue(rv)
	}

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

func formatClickHouseArrayParamValue(values reflect.Value) string {
	items := make([]string, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		items = append(items, formatClickHouseArrayLiteral(values.Index(i).Interface()))
	}

	return "[" + strings.Join(items, ",") + "]"
}

func formatClickHouseArrayLiteral(value any) string {
	if rv := reflect.ValueOf(value); rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		return formatClickHouseArrayParamValue(rv)
	}

	switch v := value.(type) {
	case nil:
		return "NULL"
	case bool:
		if v {
			return "1"
		}
		return "0"
	case string:
		return quoteClickHouseStringLiteral(v)
	default:
		return fmt.Sprint(v)
	}
}

func quoteClickHouseStringLiteral(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return "'" + escaped + "'"
}
