// Package storage provides local file storage for sandbox execution outputs.
package storage

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// File represents a stored file's metadata.
type File struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"last_modified,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Scope identifies the storage namespace for an execution's files. When a
// session is active, files are grouped under the session so a multi-turn
// session's outputs share one parent directory (<session>/<execution>/);
// otherwise they fall back to the execution ID alone.
type Scope struct {
	SessionID   string
	ExecutionID string
}

// UploadResult describes a stored file. Path is the file's location on the
// server host's filesystem — only directly openable when the server runs on
// the same host as the caller (e.g. the local CLI), not when storage lives in
// a container volume.
type UploadResult struct {
	Key  string
	URL  string
	Path string
}

// Service provides file storage backed by an afero filesystem.
type Service interface {
	// Upload stores a file scoped to a session/execution and returns its key,
	// public URL, and path on the server host.
	Upload(scope Scope, name string, body io.Reader) (UploadResult, error)
	// List returns files scoped to a session/execution, optionally filtered by prefix.
	List(scope Scope, prefix string) ([]File, error)
	// GetURL returns the public URL for a file scoped to a session/execution.
	GetURL(scope Scope, key string) string
	// ServeFile serves a stored file over HTTP.
	ServeFile(w http.ResponseWriter, r *http.Request, filePath string)
}

type service struct {
	fs      afero.Fs
	baseDir string
	baseURL string
}

// Compile-time interface check.
var _ Service = (*service)(nil)

// New creates a new storage service.
//
// fs is the filesystem implementation (afero.OsFs for production, afero.MemMapFs for tests).
// baseDir is the root directory for stored files.
// baseURL is the server's public base URL used to construct file URLs.
func New(fs afero.Fs, baseDir, baseURL string) Service {
	return &service{
		fs:      fs,
		baseDir: baseDir,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// Upload stores a file and returns its key, public URL, and host path.
func (s *service) Upload(scope Scope, name string, body io.Reader) (UploadResult, error) {
	rel, err := relativeKey(scope, name)
	if err != nil {
		return UploadResult{}, err
	}

	dir := filepath.Join(s.baseDir, filepath.FromSlash(scope.path()))
	if err := s.fs.MkdirAll(dir, 0o755); err != nil {
		return UploadResult{}, fmt.Errorf("creating storage directory: %w", err)
	}

	filePath := filepath.Join(dir, filepath.FromSlash(rel))

	// Write to a temp file in the same directory and rename it over the
	// destination only once the copy fully succeeds, so a failed or
	// interrupted upload (a normal occurrence: the source is a live sandbox
	// HTTP stream that can break mid-transfer) can never truncate or corrupt
	// whatever was already stored at this key. Same directory keeps the
	// rename on one filesystem, which is what makes it atomic.
	tmp, err := afero.TempFile(s.fs, dir, ".upload-*")
	if err != nil {
		return UploadResult{}, fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()
		_ = s.fs.Remove(tmpPath)

		return UploadResult{}, fmt.Errorf("writing file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = s.fs.Remove(tmpPath)

		return UploadResult{}, fmt.Errorf("closing file: %w", err)
	}

	if err := s.fs.Rename(tmpPath, filePath); err != nil {
		_ = s.fs.Remove(tmpPath)

		return UploadResult{}, fmt.Errorf("finalizing file: %w", err)
	}

	return UploadResult{
		Key:  rel,
		URL:  s.fileURL(scope, rel),
		Path: filePath,
	}, nil
}

// List returns files for an execution, optionally filtered by prefix.
func (s *service) List(scope Scope, prefix string) ([]File, error) {
	dir := filepath.Join(s.baseDir, filepath.FromSlash(scope.path()))
	files := make([]File, 0, 16)

	err := afero.Walk(s.fs, dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Normalize to forward slashes for consistent keys.
		rel = filepath.ToSlash(rel)

		if prefix != "" && !strings.HasPrefix(rel, prefix) {
			return nil
		}

		files = append(files, File{
			Key:          rel,
			Size:         info.Size(),
			LastModified: info.ModTime().UTC().Format(time.RFC3339),
			URL:          s.fileURL(scope, rel),
		})

		return nil
	})

	if err != nil {
		// Directory doesn't exist yet — no files uploaded for this execution.
		if errors.Is(err, os.ErrNotExist) {
			return []File{}, nil
		}

		return nil, fmt.Errorf("listing files: %w", err)
	}

	return files, nil
}

// GetURL returns the public URL for a stored file.
func (s *service) GetURL(scope Scope, key string) string {
	rel, err := relativeKey(scope, key)
	if err != nil {
		return ""
	}

	return s.fileURL(scope, rel)
}

// ServeFile serves a stored file from the filesystem.
func (s *service) ServeFile(w http.ResponseWriter, r *http.Request, filePath string) {
	fullPath := filepath.Clean(filepath.Join(s.baseDir, filepath.FromSlash(filePath)))

	// Prevent path traversal — resolved path must stay under baseDir.
	if !strings.HasPrefix(fullPath, filepath.Clean(s.baseDir)+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}

	f, err := s.fs.Open(fullPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Use http.ServeContent for proper range/caching support.
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
		return
	}

	// Fallback: stream the file.
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	_, _ = io.Copy(w, f)
}

// fileURL constructs the public URL for a stored file.
func (s *service) fileURL(scope Scope, key string) string {
	return s.baseURL + "/api/v1/storage/files/" + scope.path() + "/" + key
}

// path returns the scope's storage prefix as a forward-slash relative path:
// "<session>/<execution>" when a session is set, otherwise "<execution>".
func (sc Scope) path() string {
	exec := sanitize(sc.ExecutionID)

	if sess := sanitize(sc.SessionID); sess != "" {
		return sess + "/" + exec
	}

	return exec
}

// sanitize strips leading slashes, whitespace, and path traversal components from a path segment.
func sanitize(s string) string {
	cleaned := strings.TrimLeft(strings.TrimSpace(s), "/")
	// Reject any path traversal attempts.
	cleaned = filepath.Clean(cleaned)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return ""
	}

	return cleaned
}

// relativeKey normalizes a key, stripping the scope prefix if present.
func relativeKey(scope Scope, key string) (string, error) {
	trimmed := sanitize(key)
	if trimmed == "" {
		return "", fmt.Errorf("key is required")
	}

	// Strip the scope prefix if the caller included it.
	prefix := scope.path() + "/"
	trimmed = strings.TrimPrefix(trimmed, prefix)

	return trimmed, nil
}
