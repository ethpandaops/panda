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

	// RenderHTML lets text/html serve inline instead of being forced to
	// download. Enable it ONLY once the bucket's public domain carries a
	// Cloudflare Transform Rule that sets
	//   Content-Security-Policy: sandbox allow-scripts allow-downloads
	// on text/html responses. That header pins uploaded HTML to an opaque
	// origin (no cookies, no credentialed same-origin fetch, can't act as the
	// bucket domain), which is what makes rendering shared HTML diagnostics
	// safe. Without the rule, inline HTML would execute with full privileges
	// on the bucket origin — so this defaults off and forces download.
	RenderHTML bool
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

	// Derive TLS from the endpoint scheme (https unless explicitly http://) so a
	// local/dev http endpoint works instead of being forced to TLS.
	secure := !strings.HasPrefix(strings.ToLower(cfg.Endpoint), "http://")
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "https://"), "http://")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretKey, ""),
		Secure: secure,
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

	name := sanitizeUploadName(r.URL.Query().Get("name"))

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

	opts := minio.PutObjectOptions{ContentType: contentType}
	// Types a browser would execute in the bucket origin (HTML, SVG, JS) are
	// forced to download — defuses phishing/XSS on a public bucket while charts,
	// PDFs and text render inline. The one exception is text/html when RenderHTML
	// is on: the operator has confirmed the Cloudflare CSP-sandbox rule, so HTML
	// diagnostics can render inside that opaque origin instead of downloading.
	if h.forceDownload(contentType) {
		opts.ContentDisposition = fmt.Sprintf("attachment; filename=%q", name)
	}

	if _, err := h.client.PutObject(r.Context(), h.cfg.Bucket, key, tmp, size, opts); err != nil {
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

// maxUploadNameLen bounds the object filename so keys stay well under S3 limits
// and a hostile name can't bloat the key.
const maxUploadNameLen = 128

// sanitizeUploadName reduces a client-supplied name to a safe basename: path
// components stripped, restricted to [A-Za-z0-9._-], and length-capped while
// preserving the extension (which drives Content-Type).
func sanitizeUploadName(raw string) string {
	name := path.Base(strings.TrimSpace(raw))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "file"
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	name = strings.Trim(b.String(), ".")
	if name == "" {
		return "file"
	}

	if len(name) > maxUploadNameLen {
		ext := path.Ext(name)
		if len(ext) < maxUploadNameLen {
			name = name[:maxUploadNameLen-len(ext)] + ext
		} else {
			name = name[:maxUploadNameLen]
		}
	}

	return name
}

// forceDownload reports whether an object must be served as an attachment
// rather than rendered inline. Scriptable/markup types download by default;
// text/html is the sole opt-in exception, gated on RenderHTML (which asserts the
// bucket's CSP-sandbox rule is in place). SVG stays download-only regardless,
// since a top-level image/svg+xml can execute script and the html-scoped CSP
// rule does not cover it.
func (h *UploadsHandler) forceDownload(contentType string) bool {
	if inlineSafe(contentType) {
		return false
	}

	// text/html is the sole opt-in exception once the CSP-sandbox rule is set.
	if h.cfg.RenderHTML && baseContentType(contentType) == "text/html" {
		return false
	}

	return true
}

// baseContentType strips any parameters (e.g. "; charset=utf-8") and lowercases.
func baseContentType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}

	return ct
}

// inlineSafe reports whether a Content-Type is inert enough to serve inline from
// a public bucket unconditionally. Scriptable/markup types (html, svg, xml, js)
// are not — they are governed by forceDownload.
func inlineSafe(contentType string) bool {
	ct := baseContentType(contentType)

	switch ct {
	case "application/pdf", "text/plain", "text/csv", "text/markdown", "application/json":
		return true
	case "image/svg+xml":
		return false
	default:
		return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/")
	}
}
