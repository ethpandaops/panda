package resource

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/consensusspecs"
	"github.com/ethpandaops/panda/pkg/eips"
	"github.com/ethpandaops/panda/pkg/surface"
	"github.com/ethpandaops/panda/runbooks"
)

// RegisterRefResources registers resource templates for the ref-based content
// retrieval used by search results. Each ref URI maps to a registry lookup
// that returns the full document content.
func RegisterRefResources(
	log logrus.FieldLogger,
	reg Registry,
	runbookReg *runbooks.Registry,
	eipReg *eips.Registry,
	specsReg *consensusspecs.Registry,
) {
	log = log.WithField("component", "ref_resources")

	// runbooks://{name}
	runbookPattern := regexp.MustCompile(`^runbooks://([a-zA-Z0-9_-]+)$`)
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate(
			"runbooks://{name}",
			"Runbook Content",
			mcp.WithTemplateDescription("Full content of an investigation runbook identified by name"),
			mcp.WithTemplateMIMEType("text/markdown"),
		),
		Pattern: runbookPattern,
		Handler: func(_ context.Context, uri string, _ surface.Dialect) (string, error) {
			matches := runbookPattern.FindStringSubmatch(uri)
			if len(matches) < 2 {
				return "", fmt.Errorf("invalid runbook ref: %s", uri)
			}

			rb := runbookReg.Get(matches[1])
			if rb == nil {
				return "", fmt.Errorf("runbook not found: %s", matches[1])
			}

			return rb.Content, nil
		},
	})

	// eips://{number}
	eipPattern := regexp.MustCompile(`^eips://(\d+)$`)
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate(
			"eips://{number}",
			"EIP Content",
			mcp.WithTemplateDescription("Full content of an Ethereum Improvement Proposal identified by number"),
			mcp.WithTemplateMIMEType("text/markdown"),
		),
		Pattern: eipPattern,
		Handler: func(_ context.Context, uri string, _ surface.Dialect) (string, error) {
			matches := eipPattern.FindStringSubmatch(uri)
			if len(matches) < 2 {
				return "", fmt.Errorf("invalid eip ref: %s", uri)
			}

			num, err := strconv.Atoi(matches[1])
			if err != nil {
				return "", fmt.Errorf("invalid eip number: %s", matches[1])
			}

			for _, eip := range eipReg.All() {
				if eip.Number == num {
					return eip.Content, nil
				}
			}

			return "", fmt.Errorf("EIP-%d not found", num)
		},
	})

	// consensus-specs://{fork}/{topic}
	specPattern := regexp.MustCompile(`^consensus-specs://([a-zA-Z0-9_-]+)/([a-zA-Z0-9_-]+)$`)
	reg.RegisterTemplate(TemplateResource{
		Template: mcp.NewResourceTemplate(
			"consensus-specs://{fork}/{topic}",
			"Consensus Spec Content",
			mcp.WithTemplateDescription("Full content of a consensus-specs document identified by fork and topic"),
			mcp.WithTemplateMIMEType("text/markdown"),
		),
		Pattern: specPattern,
		Handler: func(_ context.Context, uri string, _ surface.Dialect) (string, error) {
			matches := specPattern.FindStringSubmatch(uri)
			if len(matches) < 3 {
				return "", fmt.Errorf("invalid spec ref: %s", uri)
			}

			spec, ok := specsReg.GetSpec(matches[1], matches[2])
			if !ok {
				return "", fmt.Errorf("consensus spec not found: %s/%s", matches[1], matches[2])
			}

			return spec.Content, nil
		},
	})

	log.WithFields(logrus.Fields{
		"templates": []string{
			"runbooks://{name}",
			"eips://{number}",
			"consensus-specs://{fork}/{topic}",
		},
	}).Info("Registered ref resource templates")
}
