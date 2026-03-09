package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/operations"
	"github.com/ethpandaops/mcp/pkg/proxy"
)

var proxyHTTP = &http.Client{Timeout: 120 * time.Second}

type rawProxyResponse struct {
	Body        []byte
	ContentType string
}

// startProxy loads config, builds a proxy client, starts it, and returns a cleanup function.
func startProxy(ctx context.Context) (proxy.Client, func(), error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	pc := buildProxyClient(cfg)
	if err := pc.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("connecting to proxy: %w", err)
	}

	cleanup := func() { _ = pc.Stop(ctx) }

	return pc, cleanup, nil
}

// proxyDo makes an authenticated HTTP request through the proxy.
func proxyDo(
	ctx context.Context,
	pc proxy.Client,
	method, path string,
	body io.Reader,
	query url.Values,
	headers map[string]string,
) ([]byte, int, http.Header, error) {
	reqURL := pc.URL() + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("creating request: %w", err)
	}

	token := pc.RegisterToken("cli")
	req.Header.Set("Authorization", "Bearer "+token)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := proxyHTTP.Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Clone(), fmt.Errorf("reading response: %w", err)
	}

	return data, resp.StatusCode, resp.Header.Clone(), nil
}

// proxyGet makes an authenticated GET request through the proxy.
func proxyGet(ctx context.Context, pc proxy.Client, path string, query url.Values, headers map[string]string) ([]byte, error) {
	data, status, _, err := proxyDo(ctx, pc, http.MethodGet, path, nil, query, headers)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(data))
	}

	return data, nil
}

// proxyPost makes an authenticated POST request through the proxy.
func proxyPost(ctx context.Context, pc proxy.Client, path string, body io.Reader, query url.Values, headers map[string]string) ([]byte, error) {
	data, status, _, err := proxyDo(ctx, pc, http.MethodPost, path, body, query, headers)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(data))
	}

	return data, nil
}

func proxyPostRaw(
	ctx context.Context,
	pc proxy.Client,
	path string,
	body io.Reader,
	query url.Values,
	headers map[string]string,
) (*rawProxyResponse, error) {
	data, status, responseHeaders, err := proxyDo(ctx, pc, http.MethodPost, path, body, query, headers)
	if err != nil {
		return nil, err
	}

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(data))
	}

	return &rawProxyResponse{
		Body:        data,
		ContentType: responseHeaders.Get("Content-Type"),
	}, nil
}

func proxyOperation(ctx context.Context, pc proxy.Client, operationID string, args map[string]any) (*operations.Response, error) {
	payload, err := json.Marshal(operations.Request{Args: args})
	if err != nil {
		return nil, fmt.Errorf("marshaling operation request: %w", err)
	}

	data, err := proxyPost(
		ctx,
		pc,
		"/api/v1/operations/"+operationID,
		bytes.NewReader(payload),
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return nil, err
	}

	var response operations.Response
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decoding operation response: %w", err)
	}

	return &response, nil
}

func runProxyOperation(operationID string, args map[string]any) (*operations.Response, error) {
	ctx := context.Background()

	pc, cleanup, err := startProxy(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return proxyOperation(ctx, pc, operationID, args)
}

func proxyOperationRaw(ctx context.Context, pc proxy.Client, operationID string, args map[string]any) (*rawProxyResponse, error) {
	payload, err := json.Marshal(operations.Request{Args: args})
	if err != nil {
		return nil, fmt.Errorf("marshaling operation request: %w", err)
	}

	return proxyPostRaw(
		ctx,
		pc,
		"/api/v1/operations/"+operationID,
		bytes.NewReader(payload),
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
}

func runProxyOperationRaw(operationID string, args map[string]any) (*rawProxyResponse, error) {
	ctx := context.Background()

	pc, cleanup, err := startProxy(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return proxyOperationRaw(ctx, pc, operationID, args)
}

// dsHeader returns a header map with the X-Datasource header set.
func dsHeader(name string) map[string]string {
	return map[string]string{"X-Datasource": name}
}

// printJSONBytes pretty-prints raw JSON bytes.
func printJSONBytes(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Println(string(data))

		return nil
	}

	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(string(data))

		return nil
	}

	fmt.Println(string(out))

	return nil
}
