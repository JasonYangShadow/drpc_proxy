package redis

import (
	"context"
	"time"

	"drpc_proxy.com/internal"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

// redisClient is the subset of *redis.Client used by Store.
type redisClient interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	Close() error
}

// pipelineCapable is optionally implemented by the real redis client.
type pipelineCapable interface {
	Pipelined(ctx context.Context, fn func(redis.Pipeliner) error) ([]redis.Cmder, error)
}

type Store struct {
	client redisClient
}

func NewStore(addr string) *Store {
	return &Store{
		client: redis.NewClient(&redis.Options{
			Addr:         addr,
			PoolSize:     internal.RedisPoolSize,
			MinIdleConns: internal.RedisMinIdleConns,
			MaxRetries:   internal.RedisMaxRetries,
			DialTimeout:  internal.RedisDialTimeout,
			ReadTimeout:  internal.RedisReadTimeout,
			WriteTimeout: internal.RedisWriteTimeout,
		}),
	}
}

// SaveResponse stores unified response (status, result, or error)
func (s *Store) SaveResponse(ctx context.Context, resp *internal.Response, ttl time.Duration) error {
	resp.UpdatedAt = time.Now().Unix()
	b, err := sonic.Marshal(resp)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, "rpc:"+resp.RequestID, b, ttl).Err()
}

// SaveResponses stores multiple responses in a single pipeline round-trip when
// the underlying client supports pipelining, otherwise falls back to sequential
// Set calls (e.g. in tests using a fake client).
func (s *Store) SaveResponses(ctx context.Context, resps []*internal.Response, ttl time.Duration) error {
	now := time.Now().Unix()
	if pc, ok := s.client.(pipelineCapable); ok {
		_, err := pc.Pipelined(ctx, func(pipe redis.Pipeliner) error {
			for _, resp := range resps {
				resp.UpdatedAt = now
				b, err := sonic.Marshal(resp)
				if err != nil {
					return err
				}
				pipe.Set(ctx, "rpc:"+resp.RequestID, b, ttl)
			}
			return nil
		})
		return err
	}
	// Fallback: sequential sets (used by fakes in tests).
	for _, resp := range resps {
		resp.UpdatedAt = now
		b, err := sonic.Marshal(resp)
		if err != nil {
			return err
		}
		if err := s.client.Set(ctx, "rpc:"+resp.RequestID, b, ttl).Err(); err != nil {
			return err
		}
	}
	return nil
}

// GetResponse retrieves unified response
func (s *Store) GetResponse(ctx context.Context, requestID string) (*internal.Response, error) {
	data, err := s.client.Get(ctx, "rpc:"+requestID).Bytes()
	if err != nil {
		return nil, err
	}
	var resp internal.Response
	err = sonic.Unmarshal(data, &resp)
	return &resp, err
}

func (s *Store) Close(ctx context.Context) error {
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- s.client.Close()
	}()

	select {
	case err := <-doneCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
