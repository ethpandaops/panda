package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/sirupsen/logrus"
)

// DefaultMaxObjectBytes caps a single upload at 100 MiB.
const DefaultMaxObjectBytes int64 = 100 << 20

// UploadsConfig configures the R2 (S3-compatible) bucket backing the /uploads route.
type UploadsConfig struct {
	Bucket         string
	KeyPrefix      string
	PublicBaseURL  string
	Endpoint       string
	AccessKeyID    string
	SecretKey      string
	MaxObjectBytes int64
}

// UploadsHandler streams request bodies to an R2 bucket and returns a durable
// public URL. It is the credentialed half of `panda upload` — R2 keys never
// leave the proxy.
type UploadsHandler struct {
	log    logrus.FieldLogger
	client *minio.Client
	cfg    UploadsConfig
}

// uploadResponse is the JSON relayed back through the server to the CLI.
type uploadResponse struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// NewUploadsHandler builds an R2-backed uploads handler.
func NewUploadsHandler(log logrus.FieldLogger, cfg UploadsConfig) (*UploadsHandler, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" || cfg.PublicBaseURL == "" {
		return nil, fmt.Errorf("uploads: bucket, endpoint and public_base_url are required")
	}

	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "https://"), "http://")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretKey, ""),
		Secure: true,
	})
	if err != nil {
		return nil, fmt.Errorf("uploads: creating r2 client: %w", err)
	}

	if cfg.MaxObjectBytes <= 0 {
		cfg.MaxObjectBytes = DefaultMaxObjectBytes
	}

	return &UploadsHandler{log: log.WithField("handler", "uploads"), client: client, cfg: cfg}, nil
}

// ServeHTTP accepts POST /uploads with the raw body and ?name=&content_type=.
func (h *UploadsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := path.Base(strings.TrimSpace(r.URL.Query().Get("name")))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}

	// Content-address the object so identical bytes dedup and every URL is
	// immutable; the hash needs the whole body, so buffer to a temp file first.
	tmp, err := os.CreateTemp("", "panda-upload-*")
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "creating temp file", err)
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), http.MaxBytesReader(w, r.Body, h.cfg.MaxObjectBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("object exceeds %d bytes", h.cfg.MaxObjectBytes), nil)
			return
		}
		h.fail(w, http.StatusBadRequest, "reading body", err)
		return
	}
	if size == 0 {
		h.fail(w, http.StatusBadRequest, "empty body", nil)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		h.fail(w, http.StatusInternalServerError, "rewinding temp file", err)
		return
	}

	contentType := strings.TrimSpace(r.URL.Query().Get("content_type"))
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	key := h.cfg.KeyPrefix + hex.EncodeToString(hasher.Sum(nil))[:6] + "/" + name

	if _, err := h.client.PutObject(r.Context(), h.cfg.Bucket, key, tmp, size, minio.PutObjectOptions{
		ContentType: contentType,
	}); err != nil {
		h.fail(w, http.StatusBadGateway, "putting object", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uploadResponse{
		URL:         strings.TrimRight(h.cfg.PublicBaseURL, "/") + "/" + key,
		Key:         key,
		Size:        size,
		ContentType: contentType,
	})
}

func (h *UploadsHandler) fail(w http.ResponseWriter, status int, msg string, err error) {
	if err != nil {
		h.log.WithError(err).Warn(msg)
	}
	http.Error(w, msg, status)
}
