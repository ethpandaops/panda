package resource

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestRegisterRefResourcesSkipsNilRegistries(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	reg := NewRegistry(log)
	RegisterRefResources(log, reg, nil, nil, nil)

	require.Empty(t, reg.ListTemplates())
}
