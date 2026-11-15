package outbox

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

const publishTimeout = 10 * time.Second

// KafkaPublisher publishes to Redpanda (Kafka protocol). Topics are created on first use.
type KafkaPublisher struct {
	client *kgo.Client
	admin  *kadm.Client
	mu     sync.Mutex
	topics map[string]bool
}

// NewKafkaPublisher connects lazily to the comma-separated broker list; an unreachable broker shows up as publish
// errors (the rows stay pending), not as a start-up failure.
func NewKafkaPublisher(brokers string) (*KafkaPublisher, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...), kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)))
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{client: client, admin: kadm.NewClient(client), topics: map[string]bool{}}, nil
}

func (k *KafkaPublisher) Close() { k.client.Close() }

func (k *KafkaPublisher) ensureTopic(ctx context.Context, topic string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.topics[topic] {
		return nil
	}
	resp, err := k.admin.CreateTopics(ctx, 1, 1, nil, topic)
	if err != nil {
		return err
	}
	for _, r := range resp {
		if r.Err != nil && !errors.Is(r.Err, kerr.TopicAlreadyExists) {
			return r.Err
		}
	}
	k.topics[topic] = true
	return nil
}

// Publish implements Publisher.
func (k *KafkaPublisher) Publish(ctx context.Context, topic, key string, value []byte, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := k.ensureTopic(ctx, topic); err != nil {
		return err
	}
	rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: value}
	for name, v := range headers {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: name, Value: []byte(v)})
	}
	return k.client.ProduceSync(ctx, rec).FirstErr()
}
