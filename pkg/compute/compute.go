// Package compute provides a typed Go client and models generated from the
// compute service's OpenAPI 3 specification.
package compute

import _ "embed"

//go:generate go tool oapi-codegen -config codegen.yaml openapi.yaml

// OpenAPIYAML is the checked-in OpenAPI 3 specification for the compute service.
//
//go:embed openapi.yaml
var OpenAPIYAML []byte
