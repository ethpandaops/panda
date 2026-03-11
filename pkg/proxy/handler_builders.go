package proxy

import (
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

func newClickHouseHandler(log logrus.FieldLogger, configs []ClickHouseDatasourceConfig) *handlers.ClickHouseHandler {
	if len(configs) == 0 {
		return nil
	}

	handlerConfigs := make([]handlers.ClickHouseDatasourceConfig, len(configs))
	for i, cfg := range configs {
		handlerConfigs[i] = handlers.ClickHouseDatasourceConfig{
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

	return handlers.NewClickHouseHandler(log, handlerConfigs)
}

func newPrometheusHandler(log logrus.FieldLogger, configs []PrometheusInstanceConfig) *handlers.PrometheusHandler {
	if len(configs) == 0 {
		return nil
	}

	handlerConfigs := make([]handlers.PrometheusConfig, len(configs))
	for i, cfg := range configs {
		handlerConfigs[i] = handlers.PrometheusConfig{
			Name:        cfg.Name,
			Description: cfg.Description,
			URL:         cfg.URL,
			Username:    cfg.Username,
			Password:    cfg.Password,
		}
	}

	return handlers.NewPrometheusHandler(log, handlerConfigs)
}

func newLokiHandler(log logrus.FieldLogger, configs []LokiInstanceConfig) *handlers.LokiHandler {
	if len(configs) == 0 {
		return nil
	}

	handlerConfigs := make([]handlers.LokiConfig, len(configs))
	for i, cfg := range configs {
		handlerConfigs[i] = handlers.LokiConfig{
			Name:        cfg.Name,
			Description: cfg.Description,
			URL:         cfg.URL,
			Username:    cfg.Username,
			Password:    cfg.Password,
		}
	}

	return handlers.NewLokiHandler(log, handlerConfigs)
}

func newS3Handler(log logrus.FieldLogger, cfg *S3Config) *handlers.S3Handler {
	if cfg == nil || cfg.Endpoint == "" {
		return nil
	}

	return handlers.NewS3Handler(log, &handlers.S3Config{
		Endpoint:        cfg.Endpoint,
		AccessKey:       cfg.AccessKey,
		SecretKey:       cfg.SecretKey,
		Bucket:          cfg.Bucket,
		Region:          cfg.Region,
		PublicURLPrefix: cfg.PublicURLPrefix,
	})
}

func newEthNodeHandler(log logrus.FieldLogger, cfg *EthNodeInstanceConfig) *handlers.EthNodeHandler {
	if cfg == nil || cfg.Username == "" {
		return nil
	}

	return handlers.NewEthNodeHandler(log, handlers.EthNodeConfig{
		Username: cfg.Username,
		Password: cfg.Password,
	})
}
