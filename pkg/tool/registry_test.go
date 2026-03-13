package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
)

func TestRegistryLifecycle(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(logrus.New())

	handlerA := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return CallToolSuccess("a"), nil
	}
	handlerB := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return CallToolSuccess("b"), nil
	}

	reg.Register(Definition{Tool: mcp.NewTool("alpha"), Handler: handlerA})
	reg.Register(Definition{Tool: mcp.NewTool("beta"), Handler: handlerB})

	if got := reg.List(); len(got) != 2 {
		t.Fatalf("List() len = %d, want 2", len(got))
	}

	handler, ok := reg.Get("alpha")
	if !ok || handler == nil {
		t.Fatalf("Get(alpha) = (%v, %v), want handler", handler, ok)
	}

	defs := reg.Definitions()
	if len(defs) != 2 {
		t.Fatalf("Definitions() len = %d, want 2", len(defs))
	}

	reg.Register(Definition{Tool: mcp.NewTool("alpha"), Handler: handlerB})
	handler, ok = reg.Get("alpha")
	if !ok {
		t.Fatal("Get(alpha) after overwrite = not found, want handler")
	}

	result, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if got := registryResultText(t, result); got != "b" {
		t.Fatalf("overwritten handler result = %q, want b", got)
	}

	if _, ok := reg.Get("missing"); ok {
		t.Fatal("Get(missing) = found, want missing")
	}
}

func TestCallToolResultHelpers(t *testing.T) {
	t.Parallel()

	errResult := CallToolError(errors.New("boom"))
	if !errResult.IsError {
		t.Fatal("CallToolError() IsError = false, want true")
	}
	if got := registryResultText(t, errResult); got != "Error: boom" {
		t.Fatalf("CallToolError() text = %q, want Error: boom", got)
	}

	success := CallToolSuccess("ok")
	if success.IsError {
		t.Fatal("CallToolSuccess() IsError = true, want false")
	}
	if got := registryResultText(t, success); got != "ok" {
		t.Fatalf("CallToolSuccess() text = %q, want ok", got)
	}
}

func registryResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) != 1 {
		t.Fatalf("Content len = %d, want 1", len(result.Content))
	}

	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] = %#v, want mcp.TextContent", result.Content[0])
	}

	return text.Text
}
