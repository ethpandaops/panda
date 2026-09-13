package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/auth"
)

// clickHouseOnly returns a minimal valid datasource so workflow validation is
// exercised in isolation from the "at least one datasource" requirement.
func clickHouseOnly() []ClickHouseClusterConfig {
	return []ClickHouseClusterConfig{
		{BaseDatasourceConfig: BaseDatasourceConfig{Name: "ch"}, Host: "example.com", Port: 8123, Username: "u", Password: "p"},
	}
}

func TestWorkflowConfigValidation(t *testing.T) {
	t.Parallel()

	oidcAuth := AuthConfig{
		Mode:    AuthModeOIDC,
		Issuers: []OIDCIssuerConfig{{IssuerURL: "https://idp.example.com", ClientID: "panda"}},
	}
	oauthAuth := AuthConfig{
		Mode:      AuthModeOAuth,
		IssuerURL: "https://proxy.example.com",
		GitHub:    &auth.GitHubConfig{ClientID: "cid", ClientSecret: "sec"},
		Tokens:    auth.TokensConfig{SecretKey: "key"},
	}

	tests := []struct {
		name     string
		auth     AuthConfig
		workflow *WorkflowConfig
		wantErr  string
	}{
		{
			name:     "nil workflow is valid",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: nil,
		},
		{
			name:     "token mode https host valid",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", APIToken: "tok"},
		},
		{
			name:     "token mode trailing slash valid",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io/", APIToken: "tok"},
		},
		{
			name:     "token mode http host with port valid",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "http://localhost:8080", APIToken: "tok"},
		},
		{
			name:     "ipv6 host with port valid",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "http://[::1]:8080", APIToken: "tok"},
		},
		{
			name:     "auth_mode defaults to token and requires api_token",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io"},
			wantErr:  "workflow.api_token is required when workflow.auth_mode is token",
		},
		{
			name:     "token without api_token rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", AuthMode: "token"},
			wantErr:  "workflow.api_token is required when workflow.auth_mode is token",
		},
		{
			name:     "passthrough with oidc valid",
			auth:     oidcAuth,
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", AuthMode: "passthrough"},
		},
		{
			name:     "passthrough with oauth valid",
			auth:     oauthAuth,
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", AuthMode: "passthrough"},
		},
		{
			name:     "passthrough with api_token rejected",
			auth:     oidcAuth,
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", AuthMode: "passthrough", APIToken: "tok"},
			wantErr:  "workflow.api_token must be empty when workflow.auth_mode is passthrough",
		},
		{
			name:     "passthrough with auth none rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", AuthMode: "passthrough"},
			wantErr:  "workflow.auth_mode passthrough requires proxy auth.mode oauth or oidc",
		},
		{
			name:     "unknown auth_mode rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", AuthMode: "bogus", APIToken: "tok"},
			wantErr:  "workflow.auth_mode must be one of",
		},
		{
			name:     "path rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io/workflow", APIToken: "tok"},
			wantErr:  "workflow.url must not include a path",
		},
		{
			name:     "query rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io?x=1", APIToken: "tok"},
			wantErr:  "workflow.url must not include a query",
		},
		{
			name:     "fragment rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io#frag", APIToken: "tok"},
			wantErr:  "workflow.url must not include a fragment",
		},
		{
			name:     "non-http scheme rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "ftp://workflow.example.io", APIToken: "tok"},
			wantErr:  "workflow.url must use an http or https scheme",
		},
		{
			name:     "userinfo rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "http://user:pass@workflow.example.io", APIToken: "tok"},
			wantErr:  "workflow.url must not include userinfo",
		},
		{
			name:     "out-of-range port rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "http://workflow.example.io:70000", APIToken: "tok"},
			wantErr:  "workflow.url has an invalid port",
		},
		{
			name:     "missing host rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://", APIToken: "tok"},
			wantErr:  "workflow.url must include a host",
		},
		{
			name:     "web_url with path valid",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", APIToken: "tok", WebURL: "https://ui.example.io/app"},
		},
		{
			name:     "web_url with query rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", APIToken: "tok", WebURL: "https://ui.example.io/app?x=1"},
			wantErr:  "workflow.web_url must not include a query or fragment",
		},
		{
			name:     "web_url non-http scheme rejected",
			auth:     AuthConfig{Mode: AuthModeNone},
			workflow: &WorkflowConfig{URL: "https://workflow.example.io", APIToken: "tok", WebURL: "ftp://ui.example.io"},
			wantErr:  "workflow.web_url must use an http or https scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := ServerConfig{
				Auth:       tt.auth,
				ClickHouse: clickHouseOnly(),
				Workflow:   tt.workflow,
			}
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// A proxy dedicated to workflow-engine passthrough (the local test shape) is
// valid without any datasource; with neither it still serves nothing.
func TestWorkflowOnlyProxySatisfiesDatasourceRequirement(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{
		Auth:     AuthConfig{Mode: AuthModeNone},
		Workflow: &WorkflowConfig{URL: "https://workflow.example.io", APIToken: "tok"},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	empty := ServerConfig{Auth: AuthConfig{Mode: AuthModeNone}}
	empty.ApplyDefaults()

	err := empty.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the workflow engine, or buildoor must be configured")
}

func TestWorkflowConfigResolvedHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, WorkflowAuthModeToken, (&WorkflowConfig{}).ResolvedAuthMode())
	assert.Equal(t, WorkflowAuthModePassthrough, (&WorkflowConfig{AuthMode: "passthrough"}).ResolvedAuthMode())

	// web_url wins over url; trailing slash trimmed.
	assert.Equal(t, "https://ui.example.io", (&WorkflowConfig{URL: "https://api.example.io", WebURL: "https://ui.example.io/"}).ResolvedWebURL())
	// falls back to url when web_url is empty.
	assert.Equal(t, "https://api.example.io", (&WorkflowConfig{URL: "https://api.example.io/"}).ResolvedWebURL())
}
