package proxyserver

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerBuildersReturnNilForMissingConfig(t *testing.T) {
	log := logrus.New()

	assert.Nil(t, newClickHouseHandler(log, nil))
	assert.Nil(t, newPrometheusHandler(log, nil))

	lokiHandler, err := newLokiHandler(log, nil)
	require.NoError(t, err)
	assert.Nil(t, lokiHandler)

	assert.Nil(t, newS3Handler(log, nil))
	assert.Nil(t, newS3Handler(log, &S3Config{}))
	assert.Nil(t, newEthNodeHandler(log, nil))
	assert.Nil(t, newEthNodeHandler(log, &EthNodeInstanceConfig{}))
}

func TestHandlerBuildersCreateConfiguredHandlers(t *testing.T) {
	log := logrus.New()

	clickhouse := newClickHouseHandler(log, []ClickHouseDatasourceConfig{{
		Name: "xatu", Host: "clickhouse.example", Port: 8123,
	}})
	require.NotNil(t, clickhouse)
	assert.Contains(t, clickhouse.Datasources(), "xatu")

	prometheus := newPrometheusHandler(log, []PrometheusInstanceConfig{{
		Name: "metrics", URL: "https://prom.example",
	}})
	require.NotNil(t, prometheus)
	assert.Contains(t, prometheus.Instances(), "metrics")

	loki, err := newLokiHandler(log, []LokiInstanceConfig{{
		Name: "logs", URL: "https://logs.example",
	}})
	require.NoError(t, err)
	require.NotNil(t, loki)
	assert.Contains(t, loki.Instances(), "logs")

	s3 := newS3Handler(log, &S3Config{
		Endpoint:        "https://s3.example",
		Bucket:          "artifacts",
		PublicURLPrefix: "https://cdn.example",
	})
	require.NotNil(t, s3)
	assert.Equal(t, "artifacts", s3.Bucket())
	assert.Equal(t, "https://cdn.example", s3.PublicURLPrefix())

	ethnode := newEthNodeHandler(log, &EthNodeInstanceConfig{
		Username: "user",
		Password: "pass",
	})
	require.NotNil(t, ethnode)
}
