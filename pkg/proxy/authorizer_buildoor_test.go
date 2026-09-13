package proxy

import (
	"context"
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func newBuildoorAuthorizer(allowedOrgs []string, requiredAudience string) *Authorizer {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return NewAuthorizer(log, ServerConfig{
		Buildoor: &BuildoorProxyConfig{
			StaticToken:      "x",
			AllowedOrgs:      allowedOrgs,
			RequiredAudience: requiredAudience,
		},
	})
}

func buildoorUserCtx(groups, audiences []string) context.Context {
	return withAuthUser(context.Background(), &AuthUser{
		Subject:   "alice",
		Username:  "alice",
		Groups:    groups,
		Audiences: audiences,
	})
}

func TestBuildoorAuthorizerOrgAndAudienceGates(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		allowedOrgs      []string
		requiredAudience string
		groups           []string
		audiences        []string
		want             bool
	}{
		"org and audience present": {
			allowedOrgs:      []string{"ethpandaops:Core"},
			requiredAudience: "buildoor",
			groups:           []string{"ethpandaops", "ethpandaops:Core"},
			audiences:        []string{"panda-proxy", "buildoor"},
			want:             true,
		},
		"org ok but audience missing": {
			allowedOrgs:      []string{"ethpandaops:Core"},
			requiredAudience: "buildoor",
			groups:           []string{"ethpandaops", "ethpandaops:Core"},
			audiences:        []string{"panda-proxy"},
			want:             false,
		},
		"audience ok but org missing": {
			allowedOrgs:      []string{"ethpandaops:Core"},
			requiredAudience: "buildoor",
			groups:           []string{"sigp"},
			audiences:        []string{"panda-proxy", "buildoor"},
			want:             false,
		},
		"org gate only": {
			allowedOrgs: []string{"ethpandaops:Core"},
			groups:      []string{"ethpandaops:Core"},
			audiences:   []string{"panda-proxy"},
			want:        true,
		},
		"audience gate only": {
			requiredAudience: "buildoor",
			groups:           []string{"sigp"},
			audiences:        []string{"panda-proxy", "buildoor"},
			want:             true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := newBuildoorAuthorizer(tc.allowedOrgs, tc.requiredAudience)
			ctx := buildoorUserCtx(tc.groups, tc.audiences)

			assert.Equal(t, tc.want, a.isAllowed(ctx, "buildoor", ""))

			// The discovery advert must agree with the route decision.
			filtered := a.FilterDatasources(ctx, DatasourcesResponse{
				Buildoor: &BuildoorAdvert{Enabled: true},
			})
			assert.Equal(t, tc.want, filtered.Buildoor != nil)
		})
	}
}

func TestBuildoorAuthorizerNoUserSkipsAudienceGate(t *testing.T) {
	t.Parallel()

	// Auth mode none: no user in context → local trust, gates pass (matching
	// the org gates' behavior).
	a := newBuildoorAuthorizer([]string{"ethpandaops:Core"}, "buildoor")
	assert.True(t, a.isAllowed(context.Background(), "buildoor", ""))
}
