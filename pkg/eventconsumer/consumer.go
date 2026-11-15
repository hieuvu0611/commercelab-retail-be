// Package eventconsumer reads domain events from Redpanda for one consumer group. Delivery is at least once:
// an offset is committed only after the handler succeeded, a failing handler is retried with a pause (the
// partition waits, so the order of events is kept) and handlers must dedupe by the event id.
package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Handler processes one event. An error means "try again later"; a permanent problem with an event (for
// example a payload that can never be valid) must be logged by the handler, which then returns nil.
type Handler func(ctx context.Context, e events.DomainEvent) error

// Config names what to read.
type Config struct {
	Brokers string // comma-separated
	Group   string
	Topic   string
	// RetryDelay is the pause after a failed handler call (default 1s).
	RetryDelay time.Duration
}

const defaultRetryDelay = time.Second

func decode(value []byte) (events.DomainEvent, error) {
	var e events.DomainEvent
	if err := json.Unmarshal(value, &e); err != nil {
		return e, err
	}
	if e.EventID == "" || e.EventType == "" {
		return e, errors.New("event has no id or type")
	}
	return e, nil
}

// handleWithRetry calls h until it succeeds; false means ctx ended first.
func handleWithRetry(ctx context.Context, h Handler, e events.DomainEvent, delay time.Duration) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		err := h(ctx, e)
		if err == nil {
			return true
		}
		log.Printf("[eventconsumer] event %s (%s) failed, will retry: %v", e.EventID, e.EventType, err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
	}
}

func ensureTopic(ctx context.Context, client *kgo.Client, topic string) error {
	resp, err := kadm.NewClient(client).CreateTopics(ctx, 1, 1, nil, topic)
	if err != nil {
		return err
	}
	for _, r := range resp {
		if r.Err != nil && !errors.Is(r.Err, kerr.TopicAlreadyExists) {
			return r.Err
		}
	}
	return nil
}

// Run consumes cfg.Topic until ctx ends. A new group starts at the beginning of the topic. Messages that are not
// valid events are logged and skipped.
func Run(ctx context.Context, cfg Config, h Handler) error {
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = defaultRetryDelay
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(cfg.Brokers, ",")...),
		kgo.ConsumerGroup(cfg.Group), kgo.ConsumeTopics(cfg.Topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), kgo.DisableAutoCommit(),
	)
	if err != nil {
		return fmt.Errorf("event consumer: %w", err)
	}
	defer client.Close()
	for ctx.Err() == nil {
		// The topic may not exist before its first publisher ran; keep trying until the broker answers.
		if err := ensureTopic(ctx, client, cfg.Topic); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
	for ctx.Err() == nil {
		fetches := client.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[eventconsumer] fetch %s/%d: %v", topic, partition, err)
			}
		})
		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			e, err := decode(rec.Value)
			if err != nil {
				log.Printf("[eventconsumer] skipping a message of %s offset %d: %v", rec.Topic, rec.Offset, err)
			} else if !handleWithRetry(ctx, h, e, cfg.RetryDelay) {
				return
			}
			if err := client.CommitRecords(ctx, rec); err != nil && ctx.Err() == nil {
				log.Printf("[eventconsumer] commit %s offset %d: %v", rec.Topic, rec.Offset, err)
			}
		})
	}
	return nil
}
