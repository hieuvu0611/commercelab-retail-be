//go:build integration

package eventconsumer

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
)

func TestConsumerRetriesAFailedEventAndDeliversInOrder(t *testing.T) {
	brokers := os.Getenv("REDPANDA_BROKERS")
	if brokers == "" {
		t.Skip("REDPANDA_BROKERS is not set; run `make test-integration`")
	}
	run := time.Now().UnixNano()
	topic := fmt.Sprintf("commercelab.it%d.events", run)
	pub, err := outbox.NewKafkaPublisher(brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, id := range []string{"e1", "e2"} {
		value := fmt.Sprintf(`{"eventId":"%s","eventType":"t","aggregateType":"X","aggregateId":"A"}`, id)
		if err := pub.Publish(ctx, topic, "A", []byte(value), nil); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var seen []string
	failedOnce := false
	done := make(chan struct{})
	h := func(_ context.Context, e events.DomainEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if e.EventID == "e1" && !failedOnce {
			failedOnce = true
			return fmt.Errorf("transient")
		}
		seen = append(seen, e.EventID)
		if len(seen) == 2 {
			close(done)
		}
		return nil
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = Run(runCtx, Config{Brokers: brokers, Group: fmt.Sprintf("g%d", run), Topic: topic, RetryDelay: 50 * time.Millisecond}, h)
	}()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatalf("timed out, seen %v", seen)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen[0] != "e1" || seen[1] != "e2" || !failedOnce {
		t.Fatalf("seen = %v, failedOnce = %v", seen, failedOnce)
	}
}
