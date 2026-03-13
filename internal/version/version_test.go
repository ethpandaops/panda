package version

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUserAgentUsesVersionPrefix(t *testing.T) {
	assert.Equal(t, "panda/"+Version, UserAgent())
}

func TestTransportRoundTripInjectsUserAgentWithoutMutatingInput(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	req.Header.Set("X-Test", "value")

	var seen *http.Request
	transport := &Transport{
		Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen = r
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, seen)

	assert.Equal(t, "value", req.Header.Get("X-Test"))
	assert.Empty(t, req.Header.Get("User-Agent"))
	assert.Equal(t, "value", seen.Header.Get("X-Test"))
	assert.Equal(t, UserAgent(), seen.Header.Get("User-Agent"))
}
