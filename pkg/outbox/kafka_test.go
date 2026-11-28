package outbox

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestKafkaPublisher_PublishTimesOutWhenBrokerUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	pub, err := newKafkaPublisherWithTimeout(addr, 1*time.Second)
	if err != nil {
		t.Fatalf("newKafkaPublisherWithTimeout: %v", err)
	}
	defer pub.Close()

	start := time.Now()
	err = pub.Publish(context.Background(), "commercelab.order.events", "ord-1", []byte(`{"eventId":"evt-1"}`), nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected publish error when broker is unreachable, got nil")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Publish took %s (> 4s bound); risk of stalling outbox relay tx", elapsed)
	}
}
