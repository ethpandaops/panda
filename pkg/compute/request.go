package compute

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Request is a proxy-ready HTTP request built from an operation and a loose
// argument map.
type Request struct {
	Method string
	// Path is the upstream path with parameters substituted and the query
	// string encoded.
	Path   string
	Header http.Header
	// Body is nil when the operation takes no request body.
	Body []byte
}

// ArgError marks an argument-validation failure so callers can report a bad
// request instead of an upstream failure.
type ArgError struct {
	Err error
}

func (e *ArgError) Error() string { return e.Err.Error() }

func (e *ArgError) Unwrap() error { return e.Err }

// reservedArgs are consumed by the dispatch layer and never forwarded in a
// path, query, or body position.
var reservedArgs = map[string]bool{
	"datasource":      true,
	"idempotency_key": true,
}

// BuildRequest maps args onto the operation: declared path and query
// parameters are taken by name, idempotency_key becomes the Idempotency-Key
// header, and every remaining argument becomes a body field. Empty strings
// and zero numbers are dropped from queries, and nil and empty-string values
// from bodies, matching what a caller leaving the field unset would send.
func (o Operation) BuildRequest(args map[string]any) (*Request, error) {
	req := &Request{Method: o.Method, Header: http.Header{}}

	if key, _ := args["idempotency_key"].(string); key != "" {
		req.Header.Set("Idempotency-Key", key)
	}

	consumed := make(map[string]bool, len(reservedArgs)+len(o.PathParams)+len(o.QueryParams))
	for name := range reservedArgs {
		consumed[name] = true
	}

	path := o.Path

	for _, name := range o.PathParams {
		consumed[name] = true

		value := scalarString(args[name])
		if value == "" {
			return nil, &ArgError{Err: fmt.Errorf("%s is required", name)}
		}

		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}

	query := url.Values{}

	for _, name := range o.QueryParams {
		consumed[name] = true

		if raw, ok := args[name]; ok {
			appendQueryValue(query, name, raw)
		}
	}

	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	req.Path = path

	if !o.HasBody {
		return req, checkNoLeftoverArgs(o.Name, args, consumed)
	}

	body := make(map[string]any, len(args))

	for key, value := range args {
		if consumed[key] || value == nil {
			continue
		}

		if s, isString := value.(string); isString && s == "" {
			continue
		}

		body[key] = value
	}

	for _, field := range o.RequiredBody {
		if _, ok := body[field]; !ok {
			return nil, &ArgError{Err: fmt.Errorf("%s is required", field)}
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, &ArgError{Err: fmt.Errorf("encoding request body: %w", err)}
	}

	req.Body = encoded

	return req, nil
}

// checkNoLeftoverArgs rejects arguments a body-less operation cannot carry,
// surfacing typos that were previously dropped silently.
func checkNoLeftoverArgs(operation string, args map[string]any, consumed map[string]bool) error {
	unknown := leftoverArgs(args, consumed)
	if len(unknown) == 0 {
		return nil
	}

	return &ArgError{Err: fmt.Errorf("operation %s does not accept: %s", operation, strings.Join(unknown, ", "))}
}

func appendQueryValue(query url.Values, name string, raw any) {
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			if s := scalarString(item); s != "" {
				query.Add(name, s)
			}
		}
	case []string:
		for _, item := range value {
			if item != "" {
				query.Add(name, item)
			}
		}
	case bool:
		query.Add(name, strconv.FormatBool(value))
	default:
		// Zero numbers and empty strings are treated as unset, mirroring the
		// optional-flag semantics existing callers rely on.
		if s := scalarString(value); s != "" && s != "0" {
			query.Add(name, s)
		}
	}
}

// scalarString formats a scalar argument for a path or query position. It
// returns "" for values that cannot be carried there.
func scalarString(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case float64:
		if value == math.Trunc(value) {
			return strconv.FormatInt(int64(value), 10)
		}

		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case json.Number:
		return value.String()
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func leftoverArgs(args map[string]any, consumed map[string]bool) []string {
	var unknown []string

	for key, value := range args {
		if consumed[key] || value == nil {
			continue
		}

		unknown = append(unknown, key)
	}

	sort.Strings(unknown)

	return unknown
}
