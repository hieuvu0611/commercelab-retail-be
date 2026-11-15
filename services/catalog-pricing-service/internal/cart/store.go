package cart

import (
	"context"
	"strconv"
	"sync"

	"github.com/go-redis/redis/v8"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
)

// MemoryStore is the in-memory store for tests and the development fallback (no Redis).
type MemoryStore struct {
	mu    sync.Mutex
	carts map[string]map[string]int64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{carts: map[string]map[string]int64{}}
}

func (m *MemoryStore) Get(_ context.Context, userID string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.carts[userID]))
	for k, v := range m.carts[userID] {
		out[k] = v
	}
	return out, nil
}

func (m *MemoryStore) SetMany(_ context.Context, userID string, lines map[string]int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.carts[userID] == nil {
		m.carts[userID] = map[string]int64{}
	}
	for k, v := range lines {
		m.carts[userID][k] = v
	}
	return nil
}

func (m *MemoryStore) Remove(_ context.Context, userID, skuID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.carts[userID], skuID)
	return nil
}

// RedisStore keeps one hash per user: field = SKU id, value = quantity.
type RedisStore struct{ client *redis.Client }

func NewRedisStore(client *redis.Client) *RedisStore { return &RedisStore{client: client} }

// The v2 prefix keeps clear of the JSON string values the retired gateway cart wrote under "cart:<user>".
func redisKey(userID string) string { return "cart:v2:" + userID }

func (r *RedisStore) Get(ctx context.Context, userID string) (map[string]int64, error) {
	raw, err := r.client.HGetAll(ctx, redisKey(userID)).Result()
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	out := make(map[string]int64, len(raw))
	for sku, v := range raw {
		qty, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		out[sku] = qty
	}
	return out, nil
}

func (r *RedisStore) SetMany(ctx context.Context, userID string, lines map[string]int64) error {
	values := make(map[string]any, len(lines))
	for sku, qty := range lines {
		values[sku] = qty
	}
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, redisKey(userID), values)
	pipe.Expire(ctx, redisKey(userID), TTL)
	_, err := pipe.Exec(ctx)
	return dependency.Wrap(err)
}

func (r *RedisStore) Remove(ctx context.Context, userID, skuID string) error {
	return dependency.Wrap(r.client.HDel(ctx, redisKey(userID), skuID).Err())
}
