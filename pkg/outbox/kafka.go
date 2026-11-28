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
	client  *kgo.Client
	admin   *kadm.Client
	timeout time.Duration
	mu      sync.Mutex
	topics  map[string]bool
}

// NewKafkaPublisher connects lazily to the comma-separated broker list; an unreachable broker shows up as publish
// errors (the rows stay pending), not as a start-up failure.
func NewKafkaPublisher(brokers string) (*KafkaPublisher, error) {
	return newKafkaPublisherWithTimeout(brokers, publishTimeout)
}

func newKafkaPublisherWithTimeout(brokers string, timeout time.Duration) (*KafkaPublisher, error) {
	if timeout <= 0 {
		timeout = publishTimeout
	}
	dialTimeout := 3 * time.Second
	if timeout < dialTimeout {
		dialTimeout = timeout
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.RecordDeliveryTimeout(timeout),
		kgo.ProduceRequestTimeout(timeout),
		kgo.RetryTimeout(timeout),
		kgo.DialTimeout(dialTimeout),
		kgo.RequestRetries(2),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{
		client:  client,
		admin:   kadm.NewClient(client),
		timeout: timeout,
		topics:  map[string]bool{},
	}, nil
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

func (k *KafkaPublisher) invalidateTopic(topic string) {
	k.mu.Lock()
	delete(k.topics, topic)
	k.mu.Unlock()
}

// Publish implements Publisher.
func (k *KafkaPublisher) Publish(ctx context.Context, topic, key string, value []byte, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	if err := k.ensureTopic(ctx, topic); err != nil {
		return err
	}
	rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: value}
	for name, v := range headers {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: name, Value: []byte(v)})
	}
	if err := k.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		k.invalidateTopic(topic)
		return err
	}
	return nil
}
