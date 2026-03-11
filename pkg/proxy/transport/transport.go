package transport

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// Transport defaults for all reverse proxy handlers.
const (
	defaultDialTimeout         = 10 * time.Second
	defaultTLSHandshakeTimeout = 10 * time.Second
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
)

// newProxyTransport returns an *http.Transport with sensible defaults for
// reverse-proxying upstream datasources. Setting skipVerify disables TLS
// certificate verification on the upstream connection.
func newProxyTransport(skipVerify bool) *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipVerify, //nolint:gosec // User-configured per datasource
		},
		DialContext:         (&net.Dialer{Timeout: defaultDialTimeout}).DialContext,
		TLSHandshakeTimeout: defaultTLSHandshakeTimeout,
		MaxIdleConns:        defaultMaxIdleConns,
		MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
		IdleConnTimeout:     defaultIdleConnTimeout,
	}
}
