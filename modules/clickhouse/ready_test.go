package clickhouse

import (
	"context"
	"testing"
	"time"
)

type blockingSchemaClient struct{ *fakeSchemaClient }

func (blockingSchemaClient) WaitReady(ctx context.Context) error {
	<-ctx.Done()

	return ctx.Err()
}

func TestWaitForSchemaReadyHonorsConfiguredTimeout(t *testing.T) {
	m := New()
	m.cfg.SchemaDiscovery.ReadyTimeout = 30 * time.Millisecond
	m.schemaClient = blockingSchemaClient{&fakeSchemaClient{}}

	start := time.Now()
	err := m.WaitForSchemaReady(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error when schema never becomes ready")
	}

	if elapsed > time.Second {
		t.Fatalf("WaitForSchemaReady took %v, expected ~30ms bound", elapsed)
	}
}

func TestWaitForSchemaReadyNoClient(t *testing.T) {
	m := New()

	if err := m.WaitForSchemaReady(context.Background()); err != nil {
		t.Fatalf("expected nil with no schema client, got %v", err)
	}
}

func TestApplyDefaultsSetsReadyTimeout(t *testing.T) {
	m := New()
	m.ApplyDefaults()

	if m.cfg.SchemaDiscovery.ReadyTimeout != DefaultSchemaReadyTimeout {
		t.Fatalf("ReadyTimeout = %v, want %v", m.cfg.SchemaDiscovery.ReadyTimeout, DefaultSchemaReadyTimeout)
	}
}
