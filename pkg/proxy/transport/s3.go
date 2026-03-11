package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/sirupsen/logrus"
)

// S3Config holds S3 storage proxy configuration.
type S3Config struct {
	Endpoint        string
	AccessKey       string
	SecretKey       string
	Bucket          string
	Region          string
	PublicURLPrefix string
}

// S3Handler handles requests to S3-compatible storage.
type S3Handler struct {
	log             logrus.FieldLogger
	cfg             *S3Config
	signer          *v4.Signer
	credentials     aws.CredentialsProvider
	httpClient      *http.Client
	publicURLPrefix string
}

// NewS3Handler creates a new S3 handler.
func NewS3Handler(log logrus.FieldLogger, cfg *S3Config) *S3Handler {
	if cfg == nil {
		return nil
	}

	creds := credentials.NewStaticCredentialsProvider(
		cfg.AccessKey,
		cfg.SecretKey,
		"",
	)

	return &S3Handler{
		log:         log.WithField("handler", "s3"),
		cfg:         cfg,
		signer:      v4.NewSigner(),
		credentials: creds,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		publicURLPrefix: cfg.PublicURLPrefix,
	}
}

// ServeHTTP handles S3 requests. Path format: /s3/{bucket}/{key...}
// Supports: PUT (upload), GET (download), HEAD (metadata), DELETE
func (h *S3Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		http.Error(w, "S3 storage not configured", http.StatusServiceUnavailable)

		return
	}

	// Strip /s3 prefix and extract bucket and key from the remaining path.
	// Path format: /s3/{bucket}/{key}
	path := strings.TrimPrefix(r.URL.Path, "/s3/")
	pathParts := strings.SplitN(path, "/", 2)

	if len(pathParts) == 0 || pathParts[0] == "" {
		http.Error(w, "missing bucket name in path", http.StatusBadRequest)

		return
	}

	bucket := pathParts[0]
	key := ""

	if len(pathParts) > 1 {
		key = pathParts[1]
	}

	h.log.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
		"method": r.Method,
	}).Debug("Proxying S3 request")

	// Build the target URL.
	targetURL := fmt.Sprintf("%s/%s", strings.TrimSuffix(h.cfg.Endpoint, "/"), bucket)
	if key != "" {
		targetURL = fmt.Sprintf("%s/%s", targetURL, key)
	}

	// Copy query parameters.
	if r.URL.RawQuery != "" {
		targetURL = fmt.Sprintf("%s?%s", targetURL, r.URL.RawQuery)
	}

	// Create the proxied request.
	ctx := r.Context()

	const maxUploadSize = 100 * 1024 * 1024 // 100 MB

	var bodyBytes []byte

	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		// Buffer the body to ensure accurate Content-Length for AWS v4 signing.
		// AWS SigV4 requires consistent Content-Length between signing and sending.
		var err error

		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, maxUploadSize+1))
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusInternalServerError)
			return
		}

		if len(bodyBytes) > maxUploadSize {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	}

	var body io.Reader
	if len(bodyBytes) > 0 {
		body = bytes.NewReader(bodyBytes)
	}

	proxyReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create request: %v", err), http.StatusInternalServerError)
		return
	}

	// Set ContentLength from actual body size for accurate AWS v4 signing.
	proxyReq.ContentLength = int64(len(bodyBytes))

	// Copy relevant headers from the original request.
	for _, header := range []string{"Content-Type", "Content-MD5"} {
		if v := r.Header.Get(header); v != "" {
			proxyReq.Header.Set(header, v)
		}
	}

	// Sign the request with AWS Signature v4.
	creds, err := h.credentials.Retrieve(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to retrieve credentials: %v", err), http.StatusInternalServerError)
		return
	}

	// Calculate payload hash (SHA256 of body content).
	hash := sha256.Sum256(bodyBytes)
	payloadHash := hex.EncodeToString(hash[:])

	// Set the X-Amz-Content-Sha256 header BEFORE signing.
	// This header must be present for S3 signature validation.
	proxyReq.Header.Set("X-Amz-Content-Sha256", payloadHash)

	err = h.signer.SignHTTP(ctx, creds, proxyReq, payloadHash, "s3", h.cfg.Region, time.Now())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to sign request: %v", err), http.StatusInternalServerError)
		return
	}

	// Execute the request.
	resp, err := h.httpClient.Do(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to execute request: %v", err), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy response headers.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	// Write status code.
	w.WriteHeader(resp.StatusCode)

	// Copy response body.
	_, _ = io.Copy(w, resp.Body)
}

// GetPublicURL returns the public URL for an S3 object.
func (h *S3Handler) GetPublicURL(ctx context.Context, bucket, key string) string {
	if h.publicURLPrefix != "" {
		return fmt.Sprintf("%s/%s", strings.TrimSuffix(h.publicURLPrefix, "/"), key)
	}

	return fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(h.cfg.Endpoint, "/"), bucket, key)
}

// Bucket returns the configured bucket name.
func (h *S3Handler) Bucket() string {
	if h.cfg == nil {
		return ""
	}

	return h.cfg.Bucket
}

// PublicURLPrefix returns the public URL prefix for S3 objects.
func (h *S3Handler) PublicURLPrefix() string {
	return h.publicURLPrefix
}
