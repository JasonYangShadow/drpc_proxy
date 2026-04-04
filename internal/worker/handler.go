package worker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/redis"
	"github.com/bytedance/sonic"
)

var upstreamClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 1000,
		MaxConnsPerHost:     1000,
		IdleConnTimeout:     90 * time.Second,
	},
}

type Processor struct {
	store *redis.Store
}

func NewProcessor(store *redis.Store) *Processor {
	return &Processor{store: store}
}

func (p *Processor) Process(msg []byte) error {
	var m internal.KafkaMessage
	if err := sonic.Unmarshal(msg, &m); err != nil {
		return err
	}

	// Use pre-marshaled RPC payload
	rpcPayload := m.Raw

	// Upstream request
	upstreamCtx, upstreamCancel := context.WithTimeout(context.Background(), internal.UpstreamTimeout)
	defer upstreamCancel()

	req, err := http.NewRequestWithContext(upstreamCtx, "POST", internal.UpstreamURL, bytes.NewReader(rpcPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := upstreamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Limit response size
	limited := io.LimitReader(resp.Body, internal.MaxUpstreamResponseSize)

	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}

	// Save to Redis
	redisCtx, redisCancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
	defer redisCancel()

	return p.store.SaveWithContext(redisCtx, m.RequestID, body, internal.ResultTTL)
}
