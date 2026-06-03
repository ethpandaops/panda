package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

const clickHousePingBody = "Ok."
const defaultAutodiscoverInterval = 10 * time.Second

type clickHouseAutodiscoverTarget struct {
	baseURL string
	cluster handlers.ClickHouseConfig
}

func hasClickHouseAutodiscover(entries []ClickHouseClusterConfig) bool {
	for _, entry := range entries {
		if entry.Autodiscover {
			return true
		}
	}

	return false
}

func (s *server) startAutodiscoveryLocked(ctx context.Context) {
	if len(s.cfg.ClickHouse) == 0 || s.clickhouseHandler == nil {
		return
	}

	probeCtx, cancel := context.WithCancel(ctx)
	s.autodiscoverCancel = cancel

	for _, entry := range s.cfg.ClickHouse {
		if !entry.Autodiscover {
			continue
		}

		if s.skipStaticClickHouseAutodiscover(entry) {
			continue
		}

		s.autodiscoverWG.Add(1)
		go s.runClickHouseAutodiscover(probeCtx, entry)
	}
}

func (s *server) stopAutodiscoveryLocked() {
	if s.autodiscoverCancel != nil {
		s.autodiscoverCancel()
		s.autodiscoverCancel = nil
	}

	s.autodiscoverWG.Wait()
}

func (s *server) runClickHouseAutodiscover(ctx context.Context, entry ClickHouseClusterConfig) {
	defer s.autodiscoverWG.Done()

	present := false
	s.reconcileClickHouseAutodiscover(ctx, entry, &present)

	interval := entry.AutodiscoverInterval
	if interval <= 0 {
		interval = defaultAutodiscoverInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if present {
				s.removeAutodiscoveredClickHouseCluster(entry.Name)
			}

			return
		case <-ticker.C:
			s.reconcileClickHouseAutodiscover(ctx, entry, &present)
		}
	}
}

func (s *server) reconcileClickHouseAutodiscover(ctx context.Context, entry ClickHouseClusterConfig, present *bool) {
	logger := s.log.WithFields(logrus.Fields{
		"datasource": entry.Name,
		"host":       entry.Host,
		"port":       entry.Port,
		"database":   entry.Database,
		"type":       "clickhouse",
	})

	available, target, err := s.clickHouseAutodiscoverAvailable(ctx, entry)
	if ctx.Err() != nil {
		return
	}

	if available && !*present {
		if !s.addAutodiscoveredClickHouseCluster(target.cluster) {
			return
		}

		*present = true
		logger.Info("ClickHouse autodiscovery datasource became available")

		return
	}

	if !available && *present {
		s.removeAutodiscoveredClickHouseCluster(entry.Name)
		*present = false
		logger.Info("ClickHouse autodiscovery datasource became unavailable")

		return
	}

	if err != nil {
		logger.WithError(err).Debug("ClickHouse autodiscovery probe failed")

		return
	}

	logger.WithField("available", available).Debug("ClickHouse autodiscovery state unchanged")
}

func (s *server) clickHouseAutodiscoverAvailable(ctx context.Context, entry ClickHouseClusterConfig) (bool, clickHouseAutodiscoverTarget, error) {
	target, err := newClickHouseAutodiscoverTarget(entry)
	if err != nil {
		return false, clickHouseAutodiscoverTarget{}, err
	}

	if err := s.clickHousePing(ctx, target, entry); err != nil {
		return false, target, err
	}

	exists, err := s.clickHouseDatabaseExists(ctx, target, entry)
	if err != nil {
		return false, target, err
	}

	return exists, target, nil
}

func (s *server) clickHousePing(ctx context.Context, target clickHouseAutodiscoverTarget, entry ClickHouseClusterConfig) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.baseURL+"/ping", nil)
	if err != nil {
		return err
	}

	setAutodiscoverBasicAuth(req, entry)

	resp, err := s.autodiscoverHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readLimitedResponseBody(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping returned HTTP %d: %s", resp.StatusCode, body)
	}

	if strings.TrimSpace(body) != clickHousePingBody {
		return fmt.Errorf("ping returned %q", strings.TrimSpace(body))
	}

	return nil
}

func (s *server) clickHouseDatabaseExists(ctx context.Context, target clickHouseAutodiscoverTarget, entry ClickHouseClusterConfig) (bool, error) {
	query := fmt.Sprintf("SELECT 1 FROM system.databases WHERE name = %s LIMIT 1", clickHouseSQLString(entry.Database))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.baseURL+"/", strings.NewReader(query))
	if err != nil {
		return false, err
	}

	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	setAutodiscoverBasicAuth(req, entry)

	resp, err := s.autodiscoverHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readLimitedResponseBody(resp.Body)
	if err != nil {
		return false, err
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("database check returned HTTP %d: %s", resp.StatusCode, body)
	}

	return strings.TrimSpace(body) == "1", nil
}

func newClickHouseAutodiscoverTarget(entry ClickHouseClusterConfig) (clickHouseAutodiscoverTarget, error) {
	host := strings.TrimSpace(entry.Host)
	if host == "" {
		return clickHouseAutodiscoverTarget{}, fmt.Errorf("autodiscover host is required")
	}

	port := entry.Port
	if port <= 0 {
		return clickHouseAutodiscoverTarget{}, fmt.Errorf("autodiscover port must be positive")
	}

	scheme := "http"
	if entry.Secure {
		scheme = "https"
	}
	base := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}

	return clickHouseAutodiscoverTarget{
		baseURL: strings.TrimRight(base.String(), "/"),
		cluster: handlers.ClickHouseConfig{
			Name:        entry.Name,
			Description: entry.Description,
			Host:        host,
			Port:        port,
			Database:    entry.Database,
			Username:    entry.Username,
			Password:    entry.Password,
			Secure:      entry.Secure,
			SkipVerify:  entry.SkipVerify,
			Timeout:     entry.Timeout,
		},
	}, nil
}

func setAutodiscoverBasicAuth(req *http.Request, entry ClickHouseClusterConfig) {
	if entry.Username != "" {
		req.SetBasicAuth(entry.Username, entry.Password)
	}
}

func clickHouseConfigNameSet(configs []handlers.ClickHouseConfig) map[string]struct{} {
	names := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.Name) == "" {
			continue
		}

		names[cfg.Name] = struct{}{}
	}

	return names
}

func (s *server) skipStaticClickHouseAutodiscover(entry ClickHouseClusterConfig) bool {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return false
	}

	s.autodiscoverMu.Lock()
	defer s.autodiscoverMu.Unlock()

	if _, ok := s.staticClickHouseNames[name]; !ok {
		return false
	}

	if _, warned := s.staticAutodiscoverWarns[name]; !warned {
		s.staticAutodiscoverWarns[name] = struct{}{}
		s.log.WithField("datasource", name).
			Warn("Skipping ClickHouse autodiscovery because the datasource name is statically configured")
	}

	return true
}

func (s *server) addAutodiscoveredClickHouseCluster(cfg handlers.ClickHouseConfig) bool {
	if s.clickhouseHandler == nil {
		return false
	}

	if s.skipStaticClickHouseAutodiscover(ClickHouseClusterConfig{BaseDatasourceConfig: BaseDatasourceConfig{Name: cfg.Name}}) {
		return false
	}

	s.autodiscoverMu.Lock()
	s.dynamicClickHouseNames[cfg.Name] = struct{}{}
	s.autodiscoverMu.Unlock()

	s.clickhouseHandler.AddCluster(cfg)

	return true
}

func (s *server) removeAutodiscoveredClickHouseCluster(name string) {
	s.autodiscoverMu.Lock()
	if _, ok := s.dynamicClickHouseNames[name]; !ok {
		s.autodiscoverMu.Unlock()

		return
	}
	delete(s.dynamicClickHouseNames, name)
	s.autodiscoverMu.Unlock()

	if s.clickhouseHandler != nil {
		s.clickhouseHandler.RemoveCluster(name)
	}
}

func readLimitedResponseBody(body io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func clickHouseSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
