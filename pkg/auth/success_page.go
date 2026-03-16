package auth

import "strings"

// SuccessPageConfig defines rules for customizing the OAuth success page.
// Rules are evaluated in order; the first match wins.
type SuccessPageConfig struct {
	Rules   []SuccessPageRule   `yaml:"rules,omitempty" json:"rules,omitempty"`
	Default *SuccessPageDisplay `yaml:"default,omitempty" json:"default,omitempty"`
}

// SuccessPageRule pairs a match condition with display content.
type SuccessPageRule struct {
	Match              SuccessPageMatch `yaml:"match" json:"match"`
	SuccessPageDisplay `yaml:",inline"`
}

// SuccessPageMatch defines the conditions under which a rule applies.
// All specified fields must match (AND logic).
type SuccessPageMatch struct {
	Orgs  []string `yaml:"orgs,omitempty" json:"orgs,omitempty"`
	Users []string `yaml:"users,omitempty" json:"users,omitempty"`
}

// SuccessPageDisplay holds the customizable content shown on the success page.
type SuccessPageDisplay struct {
	Tagline string            `yaml:"tagline,omitempty" json:"tagline,omitempty"`
	Media   *SuccessPageMedia `yaml:"media,omitempty" json:"media,omitempty"`
}

// SuccessPageMedia defines the media block shown on the success page.
type SuccessPageMedia struct {
	// Type is "gif" or "ascii".
	Type string `yaml:"type" json:"type"`
	// URL is the image source when Type is "gif".
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// ASCIIArtBase64 is base64-encoded ASCII art when Type is "ascii".
	ASCIIArtBase64 string `yaml:"ascii_art_base64,omitempty" json:"ascii_art_base64,omitempty"`
}

// Resolve evaluates rules against the given user and returns the display
// configuration for the first matching rule, or the default.
func (c *SuccessPageConfig) Resolve(login string, orgs []string) SuccessPageDisplay {
	if c == nil {
		return SuccessPageDisplay{}
	}

	lowerLogin := strings.ToLower(login)

	for _, rule := range c.Rules {
		if rule.matches(lowerLogin, orgs) {
			return rule.SuccessPageDisplay
		}
	}

	if c.Default != nil {
		return *c.Default
	}

	return SuccessPageDisplay{}
}

// matches returns true if the user satisfies all specified conditions.
func (r *SuccessPageRule) matches(lowerLogin string, orgs []string) bool {
	if len(r.Match.Orgs) > 0 && !matchesAnyOrg(orgs, r.Match.Orgs) {
		return false
	}

	if len(r.Match.Users) > 0 && !containsLower(r.Match.Users, lowerLogin) {
		return false
	}

	return true
}

// matchesAnyOrg returns true if the user belongs to at least one of the target orgs.
func matchesAnyOrg(userOrgs, targetOrgs []string) bool {
	for _, target := range targetOrgs {
		lower := strings.ToLower(target)

		for _, org := range userOrgs {
			if strings.ToLower(org) == lower {
				return true
			}
		}
	}

	return false
}

// containsLower returns true if needle is in the haystack (case-insensitive).
func containsLower(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.ToLower(s) == needle {
			return true
		}
	}

	return false
}
