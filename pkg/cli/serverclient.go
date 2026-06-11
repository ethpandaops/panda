package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	clickhousemodule "github.com/ethpandaops/panda/modules/clickhouse"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/operations"
)

var serverHTTP = &http.Client{Timeout: 0}

type rawServerResponse struct {
	Body        []byte
	ContentType string
}

func serverBaseURL() (string, error) {
	cfg, err := config.LoadClient(cfgFile)
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}

	return cfg.ServerURL(), nil
}

func serverDo(
	ctx context.Context,
	method, path string,
	body io.Reader,
	query url.Values,
	headers map[string]string,
) ([]byte, int, http.Header, error) {
	baseURL, err := serverBaseURL()
	if err != nil {
		return nil, 0, nil, err
	}

	reqURL := strings.TrimRight(baseURL, "/") + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("creating request: %w", err)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Attribution: callers (e.g. chat agents acting for a human user) set
	// PANDA_ON_BEHALF_OF per invocation; it travels to panda-server as
	// X-Panda-On-Behalf-Of, which the server forwards to the proxy. The
	// value is untrusted free-text used for audit only — never authorization.
	if v := strings.TrimSpace(os.Getenv("PANDA_ON_BEHALF_OF")); v != "" {
		req.Header.Set("X-Panda-On-Behalf-Of", v)
	}

	resp, err := serverHTTP.Do(req)
	if err != nil {
		if isConnectionRefused(err) {
			return nil, 0, nil, fmt.Errorf(
				"server is not running at %s — run 'panda init' or 'panda server start' first",
				baseURL,
			)
		}

		return nil, 0, nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Clone(), fmt.Errorf("reading response: %w", err)
	}

	return data, resp.StatusCode, resp.Header.Clone(), nil
}

func serverGetJSON(ctx context.Context, path string, query url.Values, target any) error {
	data, status, _, err := serverDo(ctx, http.MethodGet, path, nil, query, nil)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

func serverPostJSON(ctx context.Context, path string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	data, status, _, err := serverDo(
		ctx,
		http.MethodPost,
		path,
		bytes.NewReader(body),
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	if target == nil {
		return nil
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

func serverDelete(ctx context.Context, path string) error {
	data, status, _, err := serverDo(ctx, http.MethodDelete, path, nil, nil, nil)
	if err != nil {
		return err
	}

	if status < 200 || status >= 300 {
		return decodeAPIError(status, data)
	}

	return nil
}

func serverOperation(ctx context.Context, operationID string, args map[string]any) (*operations.Response, error) {
	var response operations.Response

	err := serverPostJSON(ctx, "/api/v1/operations/"+operationID, operations.Request{Args: args}, &response)
	if err != nil {
		return nil, err
	}

	return &response, nil
}

func serverOperationRaw(ctx context.Context, operationID string, args map[string]any) (*rawServerResponse, error) {
	body, err := json.Marshal(operations.Request{Args: args})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	data, status, responseHeaders, err := serverDo(
		ctx,
		http.MethodPost,
		"/api/v1/operations/"+operationID,
		bytes.NewReader(body),
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, decodeAPIError(status, data)
	}

	return &rawServerResponse{
		Body:        data,
		ContentType: responseHeaders.Get("Content-Type"),
	}, nil
}

func runServerOperation(cmd *cobra.Command, operationID string, args map[string]any) (*operations.Response, error) {
	return serverOperation(commandContext(cmd), operationID, args)
}

func runServerOperationRaw(cmd *cobra.Command, operationID string, args map[string]any) (*rawServerResponse, error) {
	return serverOperationRaw(commandContext(cmd), operationID, args)
}

func decodeAPIError(status int, data []byte) error {
	var message string

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err == nil {
		if msg, ok := payload["error"].(string); ok && msg != "" {
			message = msg
		}
	}

	if message == "" {
		message = strings.TrimSpace(string(data))
	}

	hint := serverErrorHint(status, message)
	if hint != "" {
		return fmt.Errorf("HTTP %d: %s\n\n  hint: %s", status, message, hint)
	}

	return fmt.Errorf("HTTP %d: %s", status, message)
}

func serverErrorHint(status int, message string) string {
	// Error classification is the ClickHouse module's knowledge; the CLI only
	// owns the command-idiom wording per class.
	switch clickhousemodule.ClassifyQueryError(message) {
	case clickhousemodule.QueryErrorPrimaryKeyFilterRequired:
		return "ClickHouse requires a query shape that can use the table's primary-key/order-key columns; inspect key clauses with 'panda schema <cluster> <database> <table>' and add selective WHERE filters on those keys. Partition filters help bound work but may not satisfy force_primary_key. Do not disable force_primary_key on an unbounded scan; only append 'SETTINGS force_primary_key=0' when the query is otherwise tightly bounded and you can explain why the key cannot be used"
	case clickhousemodule.QueryErrorUnknownIdentifier:
		return "the SQL references a column or expression that is not available in the selected table; inspect the table with 'panda schema <cluster> <database> <table>' and adjust the SELECT, WHERE, and GROUP BY clauses"
	case clickhousemodule.QueryErrorNotAggregate:
		return "ClickHouse requires every selected expression to be aggregated or included in GROUP BY; update the SELECT/GROUP BY clauses or inspect examples with 'panda search examples <topic>'"
	case clickhousemodule.QueryErrorSyntax:
		return "ClickHouse rejected the SQL syntax; confirm dataset syntax with 'panda datasets <name>' and table shape with 'panda schema <cluster> <database> <table>'. For FINAL with aliases, use 'FROM table AS alias FINAL' or wrap the FINAL read in a subquery"
	case clickhousemodule.QueryErrorDatasourceNotFound:
		return "the first ClickHouse argument must be a datasource/cluster name; list them with 'panda datasources --type clickhouse' or 'panda clickhouse list-datasources'"
	case clickhousemodule.QueryErrorDistributedJoinDenied:
		return "ClickHouse denied a distributed subquery or join; filter each side before joining, use GLOBAL/ANY JOIN when appropriate for distributed tables, or resolve the small side first and pass literal filter values into the next query"
	case clickhousemodule.QueryErrorUnknownTable:
		return "the SQL references a table or database that is not available in the selected ClickHouse datasource; list datasources with 'panda datasources --type clickhouse' and inspect tables with 'panda schema <cluster> [database]'"
	case clickhousemodule.QueryErrorUnknownFunction:
		return "the SQL uses a ClickHouse function unavailable in this deployment/version; replace it with a supported function or simplify the expression, then rerun the query"
	case clickhousemodule.QueryErrorBadFunctionArguments:
		return "a SQL function received an incompatible argument type or shape; inspect column types with 'panda schema <cluster> <database> <table>' and cast deliberately where needed"
	case clickhousemodule.QueryErrorIllegalAggregation:
		return "ClickHouse does not allow aggregate functions nested inside other aggregate functions; compute the inner aggregate in one step, then materialize or simplify before applying an outer aggregate"
	case clickhousemodule.QueryErrorAliasRequired:
		return "ClickHouse requires explicit aliases for subqueries used in JOIN/CROSS JOIN contexts; add 'AS <alias>' to each subquery or table expression"
	case clickhousemodule.QueryErrorUnknown:
	}

	normalized := strings.ToLower(message)
	if strings.Contains(normalized, "clickhouse://tables/") &&
		strings.Contains(normalized, "invalid identifier") {
		return "schema resource paths use concrete ClickHouse identifiers in /<cluster>/<database>/<table>; dataset names, placeholders, and SQL clauses are not identifiers. Read 'panda datasets <name>' for placement/syntax, then substitute concrete names"
	}

	if strings.Contains(normalized, "clickhouse://tables/") &&
		strings.Contains(normalized, "cluster ") &&
		strings.Contains(normalized, "not found") {
		return "schema expects a ClickHouse datasource/cluster name, not a dataset name; list clusters with 'panda datasources --type clickhouse'. If starting from an example, use its Target and read 'panda datasets <name>' for placement/syntax"
	}

	if strings.Contains(normalized, "unknown dataset") {
		return "datasets are knowledge-pack IDs, not datasource names; list valid datasets with 'panda datasets'. Use 'panda datasources' for datasource names and 'panda schema <cluster>' for ClickHouse table discovery"
	}

	if strings.Contains(normalized, "prometheus datasource") && strings.Contains(normalized, "not found") {
		return "the first Prometheus argument must be a live datasource name; list them with 'panda prometheus list-datasources' or 'panda datasources --type prometheus'"
	}

	switch status {
	case http.StatusNotFound:
		return "the requested module, operation, datasource, or resource is not available on this server; check 'panda datasources' and 'panda resources'"
	case http.StatusBadGateway:
		return "an upstream datasource or node returned a gateway error; the target may be temporarily unreachable — retry, or confirm it is still advertised with 'panda datasources'"
	case http.StatusServiceUnavailable:
		return "the server is running but a required service (e.g. sandbox) is not available — check server logs with 'docker compose logs server'"
	default:
		return ""
	}
}

func isConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	// Fallback: some wrapped errors don't propagate the syscall errno.
	return strings.Contains(err.Error(), "connection refused")
}
