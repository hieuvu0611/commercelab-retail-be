package outbox

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultBrokers  = "localhost:19092"
	defaultInterval = "1s"
)

// Start runs the relay of table in the background until ctx is cancelled. The brokers come from
// REDPANDA_BROKERS and the pass interval from OUTBOX_RELAY_INTERVAL (default 1s). An unreachable broker is not
// fatal: rows stay pending and are published once it is back.
func Start(ctx context.Context, service string, pool *pgxpool.Pool, table string) error {
	interval, err := time.ParseDuration(config.EnvOrDefault("OUTBOX_RELAY_INTERVAL", defaultInterval))
	if err != nil || interval < 100*time.Millisecond {
		return fmt.Errorf("OUTBOX_RELAY_INTERVAL must be a duration of at least 100ms: %v", err)
	}
	brokers := config.EnvOrDefault("REDPANDA_BROKERS", defaultBrokers)
	pub, err := NewKafkaPublisher(brokers)
	if err != nil {
		return fmt.Errorf("outbox publisher: %w", err)
	}
	go func() {
		NewRelay(pool, table, pub).Run(ctx, interval)
		pub.Close()
	}()
	log.Printf("[%s] outbox relay for %s started (brokers: %s, every %s)", service, table, brokers, interval)
	return nil
}
