package github

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubUserIsMemberOfHandlesAllowedOrganizations(t *testing.T) {
	user := &GitHubUser{Organizations: []string{"ethpandaops", "openai"}}

	assert.True(t, user.IsMemberOf(nil))
	assert.True(t, user.IsMemberOf([]string{"openai"}))
	assert.False(t, user.IsMemberOf([]string{"other"}))
}

func TestGitHubModelTypesDecodeExpectedJSON(t *testing.T) {
	var token TokenResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"access_token":"token-123",
		"token_type":"bearer",
		"scope":"read:user",
		"error":"bad_code",
		"error_description":"bad code"
	}`), &token))
	assert.Equal(t, "token-123", token.AccessToken)
	assert.Equal(t, "bad code", token.ErrorDescription)

	var user githubUserResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":42,
		"login":"octocat",
		"name":"Octo Cat",
		"email":"octo@example.com",
		"avatar_url":"https://example.com/avatar.png"
	}`), &user))
	assert.Equal(t, int64(42), user.ID)
	assert.Equal(t, "octocat", user.Login)
	assert.Equal(t, "https://example.com/avatar.png", user.AvatarURL)

	var org githubOrgResponse
	require.NoError(t, json.Unmarshal([]byte(`{"login":"ethpandaops"}`), &org))
	assert.Equal(t, "ethpandaops", org.Login)
}
