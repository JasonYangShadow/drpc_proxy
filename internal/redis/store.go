package redis

import (
	"context"
	"time"

	"drpc_proxy.com/internal"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

type Store struct {
	client *redis.Client
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
