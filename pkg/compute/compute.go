// Package compute provides a runtime-discovered interface to the compute
// service. The service publishes an OpenAPI 3 document at SpecPath; ParseSpec
// distills it into an operation index mapping public operation names to HTTP
// requests, so the full API surface is available without compiled-in bindings
// and new upstream operations need no panda change.
package compute

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
)

// SpecPath is the well-known route where the compute service serves its
// OpenAPI 3 document.
const SpecPath = "/v1/openapi.yaml"

// Operation is one indexed API operation. Only structural fields are kept:
// free-text document content (titles, descriptions, summaries) is never
// retained, so nothing service-specific can surface to callers.
type Operation struct {
	// Name is the public snake_case operation name derived from the
	// document's operationId, e.g. createSandbox -> create_sandbox.
	Name string
	// ID is the operationId as declared by the service.
	ID     string
	Method string
	// Path is the upstream route template with {param} placeholders.
	Path        string
	PathParams  []string
	QueryParams []string
	HasBody     bool
	// RequiredBody lists top-level body fields the document marks required.
	RequiredBody []string
}

// Index resolves public operation names to operations.
type Index struct {
	ops map[string]Operation
}

// legacyNames maps operation names panda published before they were derived
// from the interface document to their canonical derived names.
var legacyNames = map[string]string{
	"list_ssh_keys":  "list_ssh_public_keys",
	"add_ssh_key":    "add_ssh_public_key",
	"delete_ssh_key": "delete_ssh_public_key",
}

// ParseSpec builds an operation index from an OpenAPI 3 document.
func ParseSpec(data []byte) (*Index, error) {
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("parsing compute interface document: %w", err)
	}

	if doc.Paths == nil || doc.Paths.Len() == 0 {
		return nil, fmt.Errorf("compute interface document declares no paths")
	}

	ops := make(map[string]Operation)

	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op == nil || op.OperationID == "" {
				continue
			}

			entry := Operation{
				Name:   snakeCase(op.OperationID),
				ID:     op.OperationID,
				Method: method,
				Path:   path,
			}

			params := make(openapi3.Parameters, 0, len(item.Parameters)+len(op.Parameters))
			params = append(params, item.Parameters...)
			params = append(params, op.Parameters...)

			for _, ref := range params {
				if ref == nil || ref.Value == nil {
					continue
				}

				switch param := ref.Value; param.In {
				case openapi3.ParameterInPath:
					entry.PathParams = append(entry.PathParams, param.Name)
				case openapi3.ParameterInQuery:
					entry.QueryParams = append(entry.QueryParams, param.Name)
				}
			}

			if body := op.RequestBody; body != nil && body.Value != nil {
				entry.HasBody = true

				if media := body.Value.Content.Get("application/json"); media != nil &&
					media.Schema != nil && media.Schema.Value != nil {
					entry.RequiredBody = append(entry.RequiredBody, media.Schema.Value.Required...)
				}
			}

			ops[entry.Name] = entry
		}
	}

	return &Index{ops: ops}, nil
}

// Lookup resolves a public operation name, accepting legacy aliases.
func (i *Index) Lookup(name string) (Operation, bool) {
	if canonical, ok := legacyNames[name]; ok {
		name = canonical
	}

	op, ok := i.ops[name]

	return op, ok
}

// Operations returns the indexed operations sorted by name.
func (i *Index) Operations() []Operation {
	out := make([]Operation, 0, len(i.ops))
	for _, op := range i.ops {
		out = append(out, op)
	}

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })

	return out
}

// Names returns the sorted public operation names.
func (i *Index) Names() []string {
	names := make([]string, 0, len(i.ops))
	for name := range i.ops {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// snakeCase converts an operationId to its public snake_case name, keeping
// acronym runs intact: listSSHPublicKeys -> list_ssh_public_keys.
func snakeCase(s string) string {
	runes := []rune(s)

	var b strings.Builder

	b.Grow(len(runes) + 4)

	for i, r := range runes {
		if !unicode.IsUpper(r) {
			b.WriteRune(r)

			continue
		}

		if i > 0 {
			prevLower := unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])
			acronymEnd := unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1])

			if prevLower || acronymEnd {
				b.WriteByte('_')
			}
		}

		b.WriteRune(unicode.ToLower(r))
	}

	return b.String()
}
