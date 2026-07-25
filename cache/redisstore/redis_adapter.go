package redisstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisCommander is the commander implementation backed by a real
// redis.UniversalClient. It is the only place go-redis's wider API surface
// (Cmdable, Script, PubSub) is touched; everything else in this package talks
// to commander instead.
type redisCommander struct {
	client redis.UniversalClient
	cas    *redis.Script
}

func newRedisCommander(client redis.UniversalClient) *redisCommander {
	return &redisCommander{client: client, cas: redis.NewScript(casScript)}
}

func (c *redisCommander) getWithTTL(ctx context.Context, key string) (string, bool, time.Duration, error) {
	pipe := c.client.Pipeline()
	getCmd := pipe.Get(ctx, key)
	pttlCmd := pipe.PTTL(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return "", false, 0, err
	}
	if err := getCmd.Err(); err != nil {
		if errors.Is(err, redis.Nil) {
			return "", false, 0, nil
		}
		return "", false, 0, err
	}
	ttl := pttlCmd.Val()
	if ttl < 0 {
		ttl = 0
	}
	return getCmd.Val(), true, ttl, nil
}

func (c *redisCommander) mget(ctx context.Context, keys []string) ([]string, []bool, error) {
	values := make([]string, len(keys))
	found := make([]bool, len(keys))
	if len(keys) == 0 {
		return values, found, nil
	}
	results, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, nil, err
	}
	for i, result := range results {
		str, ok := result.(string)
		if !ok {
			continue
		}
		values[i] = str
		found[i] = true
	}
	return values, found, nil
}

func (c *redisCommander) del(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

func (c *redisCommander) incr(ctx context.Context, key string) (int64, error) {
	return c.client.Incr(ctx, key).Result()
}

func (c *redisCommander) setNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return c.client.SetNX(ctx, key, value, ttl).Result()
}

func (c *redisCommander) publish(ctx context.Context, channel, payload string) error {
	return c.client.Publish(ctx, channel, payload).Err()
}

func (c *redisCommander) evalCAS(ctx context.Context, key, payload string, ttl time.Duration, tagKeys []string, expectedVersions []int64) (bool, error) {
	keys := make([]string, 0, len(tagKeys)+1)
	keys = append(keys, key)
	keys = append(keys, tagKeys...)
	argv := make([]any, 0, len(expectedVersions)+2)
	argv = append(argv, strconv.FormatInt(ttl.Milliseconds(), 10), payload)
	for _, version := range expectedVersions {
		argv = append(argv, strconv.FormatInt(version, 10))
	}
	result, err := c.cas.Run(ctx, c.client, keys, argv...).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *redisCommander) subscribe(ctx context.Context, channel string, onMessage func(payload string)) error {
	pubsub := c.client.Subscribe(ctx, channel)
	defer pubsub.Close()
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-messages:
			if !ok {
				return errors.New("redisstore: subscription channel closed")
			}
			onMessage(msg.Payload)
		}
	}
}

func (c *redisCommander) close() error {
	return c.client.Close()
}
