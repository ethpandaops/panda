package proxyserver

import (
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy/transport"
)

func newClickHouseHandler(log logrus.FieldLogger, configs []ClickHouseDatasourceConfig) *transport.ClickHouseHandler {
	if len(configs) == 0 {
		return nil
	}

	handlerConfigs := make([]transport.ClickHouseDatasourceConfig, len(configs))
	for i, cfg := range configs {
		handlerConfigs[i] = transport.ClickHouseDatasourceConfig{
			Name:        cfg.Name,
			Description: cfg.Description,
			Host:        cfg.Host,
			Port:        cfg.Port,
			Database:    cfg.Database,
			Username:    cfg.Username,
			Password:    cfg.Password,
			Secure:      cfg.Secure,
			SkipVerify:  cfg.SkipVerify,
			Timeout:     cfg.Timeout,
		}
	}

	return transport.NewClickHouseHandler(log, handlerConfigs)
}

func newPrometheusHandler(log logrus.FieldLogger, configs []PrometheusInstanceConfig) *transport.PrometheusHandler {
	if len(configs) == 0 {
		return nil
	}

	handlerConfigs := make([]transport.PrometheusConfig, len(configs))
	for i, cfg := range configs {
		handlerConfigs[i] = transport.PrometheusConfig{
			Name:        cfg.Name,
			Description: cfg.Description,
			URL:         cfg.URL,
			Username:    cfg.Username,
			Password:    cfg.Password,
		}
	}

	return transport.NewPrometheusHandler(log, handlerConfigs)
}

func newLokiHandler(log logrus.FieldLogger, configs []LokiInstanceConfig) (*transport.LokiHandler, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	handlerConfigs := make([]transport.LokiConfig, len(configs))
	for i, cfg := range configs {
		handlerConfigs[i] = transport.LokiConfig{
			Name:        cfg.Name,
			Description: cfg.Description,
			URL:         cfg.URL,
			Username:    cfg.Username,
			Password:    cfg.Password,
		}
	}

	return transport.NewLokiHandler(log, handlerConfigs)
}

func newS3Handler(log logrus.FieldLogger, cfg *S3Config) *transport.S3Handler {
	if cfg == nil || cfg.Endpoint == "" {
		return nil
	}

	return transport.NewS3Handler(log, &transport.S3Config{
		Endpoint:        cfg.Endpoint,
		AccessKey:       cfg.AccessKey,
		SecretKey:       cfg.SecretKey,
		Bucket:          cfg.Bucket,
		Region:          cfg.Region,
		PublicURLPrefix: cfg.PublicURLPrefix,
	})
}

func newEthNodeHandler(log logrus.FieldLogger, cfg *EthNodeInstanceConfig) *transport.EthNodeHandler {
	if cfg == nil || cfg.Username == "" {
		return nil
	}

	return transport.NewEthNodeHandler(log, transport.EthNodeConfig{
		Username: cfg.Username,
		Password: cfg.Password,
	})
}
