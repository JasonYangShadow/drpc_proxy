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

type Status struct {
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updated_at"`
}

func NewStore(addr string) *Store {
	return &Store{
		client: redis.NewClient(&redis.Options{
			Addr:         addr,
			PoolSize:     100,
			MinIdleConns: 10,
			MaxRetries:   3,
			DialTimeout:  internal.RedisDialTimeout,
			ReadTimeout:  internal.RedisReadTimeout,
			WriteTimeout: internal.RedisWriteTimeout,
		}),
	}
}

func (s *Store) SaveWithContext(ctx context.Context, id string, data any, ttl time.Duration) error {
	b, err := sonic.Marshal(data)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, "rpc:result:"+id, b, ttl).Err()
}

func (s *Store) GetWithContext(ctx context.Context, id string) ([]byte, error) {
	return s.client.Get(ctx, "rpc:result:"+id).Bytes()
}

// SetStatus stores request processing status
func (s *Store) SetStatus(ctx context.Context, id string, status string, ttl time.Duration) error {
	st := Status{
		Status:    status,
		UpdatedAt: time.Now().Unix(),
	}
	b, err := sonic.Marshal(&st)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, "rpc:status:"+id, b, ttl).Err()
}

// GetStatus retrieves request processing status
func (s *Store) GetStatus(ctx context.Context, id string) (*Status, error) {
	data, err := s.client.Get(ctx, "rpc:status:"+id).Bytes()
	if err != nil {
		return nil, err
	}
	var st Status
	err = sonic.Unmarshal(data, &st)
	return &st, err
}
