package storage

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (Service, afero.Fs) {
	fs := afero.NewMemMapFs()
	log := logrus.New()
	svc := New(log, fs, "/data", "http://localhost:2480")

	return svc, fs
}

func TestUploadAndList(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	body := bytes.NewBufferString("hello world")
	key, url, err := svc.Upload("exec-123", "chart.png", body, "image/png")
	require.NoError(t, err)
	assert.Equal(t, "chart.png", key)
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/exec-123/chart.png", url)

	files, err := svc.List("exec-123", "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "chart.png", files[0].Key)
	assert.Equal(t, int64(11), files[0].Size)
	assert.Equal(t, url, files[0].URL)
}

func TestUploadSubdirectory(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	body := bytes.NewBufferString("data")
	key, _, err := svc.Upload("exec-456", "reports/output.csv", body, "text/csv")
	require.NoError(t, err)
	assert.Equal(t, "reports/output.csv", key)

	files, err := svc.List("exec-456", "reports/")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "reports/output.csv", files[0].Key)
}

func TestListEmptyExecution(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	files, err := svc.List("nonexistent", "")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestListWithPrefix(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, _, err := svc.Upload("exec-789", "charts/a.png", bytes.NewBufferString("a"), "image/png")
	require.NoError(t, err)

	_, _, err = svc.Upload("exec-789", "data/b.csv", bytes.NewBufferString("b"), "text/csv")
	require.NoError(t, err)

	files, err := svc.List("exec-789", "charts/")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "charts/a.png", files[0].Key)
}

func TestGetURL(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	url := svc.GetURL("exec-123", "chart.png")
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/exec-123/chart.png", url)
}

func TestGetURLStripsExecutionPrefix(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	url := svc.GetURL("exec-123", "exec-123/chart.png")
	assert.Equal(t, "http://localhost:2480/api/v1/storage/files/exec-123/chart.png", url)
}

func TestUploadEmptyKeyError(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, _, err := svc.Upload("exec-123", "", bytes.NewBufferString("data"), "text/plain")
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
	_, _, err := svc.Upload("exec-123", "output.txt", body, "text/plain")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/storage/files/exec-123/output.txt", nil)
	svc.ServeFile(w, r, "exec-123/output.txt")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "file content", w.Body.String())
}

func TestUploadOverwritesExisting(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, _, err := svc.Upload("exec-123", "file.txt", bytes.NewBufferString("v1"), "text/plain")
	require.NoError(t, err)

	_, _, err = svc.Upload("exec-123", "file.txt", bytes.NewBufferString("v2"), "text/plain")
	require.NoError(t, err)

	files, err := svc.List("exec-123", "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, int64(2), files[0].Size)
}

func TestIsolationBetweenExecutions(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService()

	_, _, err := svc.Upload("exec-a", "file.txt", bytes.NewBufferString("a"), "text/plain")
	require.NoError(t, err)

	_, _, err = svc.Upload("exec-b", "file.txt", bytes.NewBufferString("b"), "text/plain")
	require.NoError(t, err)

	filesA, err := svc.List("exec-a", "")
	require.NoError(t, err)
	require.Len(t, filesA, 1)

	filesB, err := svc.List("exec-b", "")
	require.NoError(t, err)
	require.Len(t, filesB, 1)
}
