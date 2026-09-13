package storage

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (Service, afero.Fs) {
	fs := afero.NewMemMapFs()
	svc := New(fs, "/data", "http://localhost:2480")

	return svc, fs
}

func TestUploadAndList(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	body := bytes.NewBufferString("hello world")
	result, err := svc.Upload(Scope{ExecutionID: "exec-123"}, "chart.png", body)
	require.NoError(t, err)
	assert.Equal(t, "chart.png", result.Key)
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/exec-123/chart.png", result.URL)
	assert.Equal(t, "/data/exec-123/chart.png", result.Path)

	files, err := svc.List(Scope{ExecutionID: "exec-123"}, "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "chart.png", files[0].Key)
	assert.Equal(t, int64(11), files[0].Size)
	assert.Equal(t, result.URL, files[0].URL)
}

func TestUploadWithSession(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	scope := Scope{SessionID: "sess-1", ExecutionID: "exec-123"}

	result, err := svc.Upload(scope, "chart.png", bytes.NewBufferString("hi"))
	require.NoError(t, err)
	assert.Equal(t, "chart.png", result.Key)
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/sess-1/exec-123/chart.png", result.URL)
	assert.Equal(t, "/data/sess-1/exec-123/chart.png", result.Path)

	// The file is listable under the same session/execution scope.
	files, err := svc.List(scope, "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "chart.png", files[0].Key)

	// An execution-only scope with the same execution ID must not see it —
	// session-scoped files live under a distinct directory.
	other, err := svc.List(Scope{ExecutionID: "exec-123"}, "")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestUploadSubdirectory(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	body := bytes.NewBufferString("data")
	result, err := svc.Upload(Scope{ExecutionID: "exec-456"}, "reports/output.csv", body)
	require.NoError(t, err)
	assert.Equal(t, "reports/output.csv", result.Key)

	files, err := svc.List(Scope{ExecutionID: "exec-456"}, "reports/")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "reports/output.csv", files[0].Key)
}

func TestListEmptyExecution(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	files, err := svc.List(Scope{ExecutionID: "nonexistent"}, "")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestListWithPrefix(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, err := svc.Upload(Scope{ExecutionID: "exec-789"}, "charts/a.png", bytes.NewBufferString("a"))
	require.NoError(t, err)

	_, err = svc.Upload(Scope{ExecutionID: "exec-789"}, "data/b.csv", bytes.NewBufferString("b"))
	require.NoError(t, err)

	files, err := svc.List(Scope{ExecutionID: "exec-789"}, "charts/")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "charts/a.png", files[0].Key)
}

func TestGetURL(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	url := svc.GetURL(Scope{ExecutionID: "exec-123"}, "chart.png")
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/exec-123/chart.png", url)
}

func TestGetURLWithSession(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	url := svc.GetURL(Scope{SessionID: "sess-1", ExecutionID: "exec-123"}, "chart.png")
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/sess-1/exec-123/chart.png", url)
}

func TestGetURLStripsScopePrefix(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	url := svc.GetURL(Scope{SessionID: "sess-1", ExecutionID: "exec-123"}, "sess-1/exec-123/chart.png")
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/sess-1/exec-123/chart.png", url)
}

func TestUploadEmptyKeyError(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, err := svc.Upload(Scope{ExecutionID: "exec-123"}, "", bytes.NewBufferString("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key is required")
}

func TestServeFileNotFound(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/storage/files/exec-123/missing.png", nil)
	svc.ServeFile(w, r, "exec-123/missing.png")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeFileSuccess(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	body := bytes.NewBufferString("file content")
	_, err := svc.Upload(Scope{ExecutionID: "exec-123"}, "output.txt", body)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/storage/files/exec-123/output.txt", nil)
	svc.ServeFile(w, r, "exec-123/output.txt")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "file content", w.Body.String())
}

func TestServeFileSessionScoped(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, err := svc.Upload(Scope{SessionID: "sess-1", ExecutionID: "exec-123"}, "output.txt", bytes.NewBufferString("v"))
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/storage/files/sess-1/exec-123/output.txt", nil)
	svc.ServeFile(w, r, "sess-1/exec-123/output.txt")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "v", w.Body.String())
}

func TestUploadOverwritesExisting(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, err := svc.Upload(Scope{ExecutionID: "exec-123"}, "file.txt", bytes.NewBufferString("v1"))
	require.NoError(t, err)

	_, err = svc.Upload(Scope{ExecutionID: "exec-123"}, "file.txt", bytes.NewBufferString("v2"))
	require.NoError(t, err)

	files, err := svc.List(Scope{ExecutionID: "exec-123"}, "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, int64(2), files[0].Size)
}

func TestIsolationBetweenExecutions(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, err := svc.Upload(Scope{ExecutionID: "exec-a"}, "file.txt", bytes.NewBufferString("a"))
	require.NoError(t, err)

	_, err = svc.Upload(Scope{ExecutionID: "exec-b"}, "file.txt", bytes.NewBufferString("b"))
	require.NoError(t, err)

	filesA, err := svc.List(Scope{ExecutionID: "exec-a"}, "")
	require.NoError(t, err)
	require.Len(t, filesA, 1)

	filesB, err := svc.List(Scope{ExecutionID: "exec-b"}, "")
	require.NoError(t, err)
	require.Len(t, filesB, 1)
}

func TestServeFilePathTraversal(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/storage/files/../../etc/passwd", nil)
	svc.ServeFile(w, r, "../../etc/passwd")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// breaksAfter is a reader that returns n bytes and then a fixed error,
// simulating a source stream (a live sandbox HTTP body, in production) that
// breaks partway through.
type breaksAfter struct {
	data []byte
	err  error
}

func (r *breaksAfter) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}

	n := copy(p, r.data)
	r.data = r.data[n:]

	return n, nil
}

func TestUploadFailureLeavesPriorFileUntouched(t *testing.T) {
	t.Parallel()

	svc, fs := newTestService()

	_, err := svc.Upload(Scope{ExecutionID: "exec-1"}, "output.txt", bytes.NewBufferString("the original good contents"))
	require.NoError(t, err)

	_, err = svc.Upload(Scope{ExecutionID: "exec-1"}, "output.txt", &breaksAfter{
		data: []byte("partial"),
		err:  assert.AnError,
	})
	require.Error(t, err)

	contents, err := afero.ReadFile(fs, "/data/exec-1/output.txt")
	require.NoError(t, err)
	assert.Equal(t, "the original good contents", string(contents),
		"a failed re-upload to the same key must not disturb the existing file")

	files, err := svc.List(Scope{ExecutionID: "exec-1"}, "")
	require.NoError(t, err)
	require.Len(t, files, 1, "no leftover temp file should remain after a failed upload")
}

func TestUploadFailureOnNewKeyLeavesNoFile(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, err := svc.Upload(Scope{ExecutionID: "exec-1"}, "output.txt", &breaksAfter{
		data: []byte("partial"),
		err:  assert.AnError,
	})
	require.Error(t, err)

	files, err := svc.List(Scope{ExecutionID: "exec-1"}, "")
	require.NoError(t, err)
	assert.Empty(t, files, "a failed upload to a brand-new key should leave nothing behind")
}
