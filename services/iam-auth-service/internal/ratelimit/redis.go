package ratelimit

import (
	"context"
	"errors"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
)

// Redis is the production Limiter: a fixed window per key, kept in Redis so every IAM replica shares it.
// The window is the key's TTL, started by the first failure.
type Redis struct {
	client *redis.Client
	max    int64
	window time.Duration
}

func NewRedis(client *redis.Client, max int, window time.Duration) *Redis {
	return &Redis{client: client, max: int64(max), window: window}
}

func (r *Redis) Blocked(ctx context.Context, key string) (bool, time.Duration, error) {
	count, err := r.client.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, dependency.Wrap(err)
	}
	if count < r.max {
		return false, 0, nil
	}
	ttl, err := r.client.TTL(ctx, key).Result()
	if err != nil {
		return false, 0, dependency.Wrap(err)
	}
	if ttl <= 0 {
		ttl = r.window
	}
	return true, ttl, nil
}

// recordScript increments the counter and sets the expiry only when the key is new, atomically.
var recordScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func (r *Redis) RecordFailure(ctx context.Context, key string) error {
	return dependency.Wrap(recordScript.Run(ctx, r.client, []string{key}, r.window.Milliseconds()).Err())
}

func (r *Redis) Reset(ctx context.Context, key string) error {
	return dependency.Wrap(r.client.Del(ctx, key).Err())
}
