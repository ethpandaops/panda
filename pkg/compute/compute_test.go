package compute

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSpec is a stand-in interface document. Its free-text fields carry the
// brandmark-xyzzy sentinel so tests can prove no document free text is
// retained in the index.
const testSpec = `
openapi: 3.0.3
info:
  title: Brandmark-Xyzzy Fabric API
  description: brandmark-xyzzy internal control plane.
  version: v1
paths:
  /v1/widgets:
    get:
      operationId: listWidgets
      description: brandmark-xyzzy widget listing.
      parameters:
        - $ref: '#/components/parameters/Limit'
        - name: filter
          in: query
          schema:
            type: array
            items:
              type: string
      responses:
        '200':
          description: OK
    post:
      operationId: createWidget
      parameters:
        - name: Idempotency-Key
          in: header
          required: true
          schema:
            type: string
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/CreateWidgetRequest'
      responses:
        '202':
          description: Accepted
  /v1/widgets/{id}:
    parameters:
      - name: id
        in: path
        required: true
        schema:
          type: string
    get:
      operationId: getWidget
      responses:
        '200':
          description: OK
    delete:
      operationId: deleteWidget
      parameters:
        - name: retain
          in: query
          schema:
            type: boolean
      responses:
        '204':
          description: Removed
  /v1/me/ssh-keys:
    get:
      operationId: listSSHPublicKeys
      responses:
        '200':
          description: OK
components:
  parameters:
    Limit:
      name: limit
      in: query
      schema:
        type: integer
  schemas:
    CreateWidgetRequest:
      type: object
      required: [name]
      properties:
        name:
          type: string
        note:
          type: string
`

func testIndex(t *testing.T) *Index {
	t.Helper()

	index, err := ParseSpec([]byte(testSpec))
	require.NoError(t, err)

	return index
}

func TestParseSpecIndexesOperations(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	assert.Equal(t, []string{
		"create_widget", "delete_widget", "get_widget", "list_ssh_public_keys", "list_widgets",
	}, index.Names())

	op, ok := index.Lookup("create_widget")
	require.True(t, ok)
	assert.Equal(t, "createWidget", op.ID)
	assert.Equal(t, "POST", op.Method)
	assert.Equal(t, "/v1/widgets", op.Path)
	assert.True(t, op.HasBody)
	assert.Equal(t, []string{"name"}, op.RequiredBody)

	op, ok = index.Lookup("delete_widget")
	require.True(t, ok)
	assert.Equal(t, []string{"id"}, op.PathParams)
	assert.Equal(t, []string{"retain"}, op.QueryParams)
	assert.False(t, op.HasBody)
}

func TestParseSpecRejectsUnusableDocuments(t *testing.T) {
	t.Parallel()

	_, err := ParseSpec([]byte("!! not yaml"))
	assert.Error(t, err)

	_, err = ParseSpec([]byte("openapi: 3.0.3\ninfo:\n  title: x\n  version: v1\npaths: {}\n"))
	assert.ErrorContains(t, err, "no paths")
}

// TestIndexRetainsNoFreeText proves the neutrality invariant: nothing from
// the document's free-text fields (titles, descriptions) survives into the
// index, so upstream branding cannot surface to callers.
func TestIndexRetainsNoFreeText(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	serialized, err := json.Marshal(index.Operations())
	require.NoError(t, err)

	assert.NotContains(t, strings.ToLower(string(serialized)), "xyzzy")
}

func TestLookupResolvesLegacyAliases(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	op, ok := index.Lookup("list_ssh_keys")
	require.True(t, ok)
	assert.Equal(t, "listSSHPublicKeys", op.ID)
}

func TestSnakeCase(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"createSandbox":      "create_sandbox",
		"listSSHPublicKeys":  "list_ssh_public_keys",
		"prepareSandboxSSH":  "prepare_sandbox_ssh",
		"authCLIConfig":      "auth_cli_config",
		"getImageRestoredBy": "get_image_restored_by",
		"meta":               "meta",
		"healthz":            "healthz",
	}

	for input, want := range cases {
		assert.Equal(t, want, snakeCase(input), "snakeCase(%s)", input)
	}
}

func TestBuildRequestSplitsArguments(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	op, ok := index.Lookup("create_widget")
	require.True(t, ok)

	req, err := op.BuildRequest(map[string]any{
		"datasource":      "production",
		"idempotency_key": "idem-1",
		"name":            "w1",
		"note":            "hello",
		"empty":           "",
		"nothing":         nil,
	})
	require.NoError(t, err)

	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/v1/widgets", req.Path)
	assert.Equal(t, "idem-1", req.Header.Get("Idempotency-Key"))
	assert.JSONEq(t, `{"name":"w1","note":"hello"}`, string(req.Body),
		"reserved, nil, and empty-string arguments stay out of the body")
}

func TestBuildRequestEnforcesRequiredBodyFields(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	op, ok := index.Lookup("create_widget")
	require.True(t, ok)

	_, err := op.BuildRequest(map[string]any{"note": "no name"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	var argErr *ArgError

	assert.True(t, errors.As(err, &argErr))
}

func TestBuildRequestSubstitutesAndEscapesPathParams(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	op, ok := index.Lookup("get_widget")
	require.True(t, ok)

	req, err := op.BuildRequest(map[string]any{"id": "name@v1/beta"})
	require.NoError(t, err)
	assert.Equal(t, "/v1/widgets/name@v1%2Fbeta", req.Path)

	_, err = op.BuildRequest(map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")
}

func TestBuildRequestQuerySemantics(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	op, ok := index.Lookup("list_widgets")
	require.True(t, ok)

	// Zero numbers and empty strings are unset; repeated values fan out.
	req, err := op.BuildRequest(map[string]any{
		"limit":  float64(0),
		"filter": []any{"state=running", "vcpu>=4"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/v1/widgets?filter=state%3Drunning&filter=vcpu%3E%3D4", req.Path)

	op, ok = index.Lookup("delete_widget")
	require.True(t, ok)

	// A false boolean is meaningful and must be sent.
	req, err = op.BuildRequest(map[string]any{"id": "w1", "retain": false, "idempotency_key": "k"})
	require.NoError(t, err)
	assert.Equal(t, "/v1/widgets/w1?retain=false", req.Path)
	assert.Nil(t, req.Body)
}

func TestBuildRequestRejectsUnknownArgsWithoutBody(t *testing.T) {
	t.Parallel()

	index := testIndex(t)

	op, ok := index.Lookup("get_widget")
	require.True(t, ok)

	_, err := op.BuildRequest(map[string]any{"id": "w1", "bogus": "x", "extra": float64(1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not accept: bogus, extra")
}
