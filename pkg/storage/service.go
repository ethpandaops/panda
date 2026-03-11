// Package storage provides local file storage for sandbox execution outputs.
package storage

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
)

// File represents a stored file's metadata.
type File struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"last_modified,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Service provides file storage backed by an afero filesystem.
type Service interface {
	// Upload stores a file scoped to an execution and returns its relative key and public URL.
	Upload(executionID, name string, body io.Reader, contentType string) (relativeKey, url string, err error)
	// List returns files scoped to an execution, optionally filtered by prefix.
	List(executionID, prefix string) ([]File, error)
	// GetURL returns the public URL for a file scoped to an execution.
	GetURL(executionID, key string) string
	// ServeFile serves a stored file over HTTP.
	ServeFile(w http.ResponseWriter, r *http.Request, filePath string)
}

type service struct {
	log     logrus.FieldLogger
	fs      afero.Fs
	baseDir string
	baseURL string
}

// New creates a new storage service.
//
// fs is the filesystem implementation (afero.OsFs for production, afero.MemMapFs for tests).
// baseDir is the root directory for stored files.
// baseURL is the server's public base URL used to construct file URLs.
func New(log logrus.FieldLogger, fs afero.Fs, baseDir, baseURL string) Service {
	return &service{
		log:     log.WithField("component", "storage"),
		fs:      fs,
		baseDir: baseDir,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// Upload stores a file and returns its relative key and public URL.
func (s *service) Upload(executionID, name string, body io.Reader, contentType string) (string, string, error) {
	relativeKey, err := relativeKey(executionID, name)
	if err != nil {
		return "", "", err
	}

	dir := filepath.Join(s.baseDir, sanitize(executionID))
	if err := s.fs.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating storage directory: %w", err)
	}

	filePath := filepath.Join(s.baseDir, sanitize(executionID), relativeKey)

	f, err := s.fs.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("creating file: %w", err)
	}

	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		return "", "", fmt.Errorf("writing file: %w", err)
	}

	if err := f.Close(); err != nil {
		return "", "", fmt.Errorf("closing file: %w", err)
	}

	url := s.fileURL(executionID, relativeKey)

	return relativeKey, url, nil
}

// List returns files for an execution, optionally filtered by prefix.
func (s *service) List(executionID, prefix string) ([]File, error) {
	dir := filepath.Join(s.baseDir, sanitize(executionID))

	exists, err := afero.DirExists(s.fs, dir)
	if err != nil {
		return nil, fmt.Errorf("checking directory: %w", err)
	}

	if !exists {
		return []File{}, nil
	}

	files := make([]File, 0, 16)

	err = afero.Walk(s.fs, dir, func(path string, info os.FileInfo, walkErr error) error {
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
			URL:          s.fileURL(executionID, rel),
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("listing files: %w", err)
	}

	return files, nil
}

// GetURL returns the public URL for a stored file.
func (s *service) GetURL(executionID, key string) string {
	rel, err := relativeKey(executionID, key)
	if err != nil {
		return ""
	}

	return s.fileURL(executionID, rel)
}

// ServeFile serves a stored file from the filesystem.
func (s *service) ServeFile(w http.ResponseWriter, r *http.Request, filePath string) {
	fullPath := filepath.Join(s.baseDir, filepath.FromSlash(filePath))

	exists, err := afero.Exists(s.fs, fullPath)
	if err != nil || !exists {
		http.NotFound(w, r)
		return
	}

	// afero doesn't directly support http.ServeFile, so read and serve manually.
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
func (s *service) fileURL(executionID, key string) string {
	return s.baseURL + "/api/v1/storage/files/" + sanitize(executionID) + "/" + key
}

// sanitize strips leading slashes and whitespace from a path component.
func sanitize(s string) string {
	return strings.TrimLeft(strings.TrimSpace(s), "/")
}

// relativeKey normalizes a key, stripping the execution prefix if present.
func relativeKey(executionID, key string) (string, error) {
	trimmed := strings.TrimLeft(strings.TrimSpace(key), "/")
	if trimmed == "" {
		return "", fmt.Errorf("key is required")
	}

	// Strip the execution prefix if the caller included it.
	prefix := sanitize(executionID) + "/"
	trimmed = strings.TrimPrefix(trimmed, prefix)

	return trimmed, nil
}
