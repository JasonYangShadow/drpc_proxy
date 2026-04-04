package worker

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/redis"
	"github.com/bytedance/sonic"
)

var upstreamClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:          internal.UpstreamMaxIdleConns,
		MaxIdleConnsPerHost:   internal.UpstreamMaxIdleConnsPerHost,
		MaxConnsPerHost:       internal.UpstreamMaxConnsPerHost,
		IdleConnTimeout:       internal.UpstreamIdleConnTimeout,
		TLSHandshakeTimeout:   internal.UpstreamTLSHandshakeTimeout,
		ResponseHeaderTimeout: internal.UpstreamResponseHeaderTimeout,
	},
}

type Processor struct {
	store  *redis.Store
	ctx    context.Context
	cancel context.CancelFunc
}

func NewProcessor(store *redis.Store) *Processor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Processor{
		store:  store,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (p *Processor) Process(msg []byte) error {
	var m internal.KafkaMessage
	if err := sonic.Unmarshal(msg, &m); err != nil {
		return err
	}

	// Use pre-marshaled RPC payload
	rpcPayload := m.Raw

	// Upstream request
	upstreamCtx, upstreamCancel := context.WithTimeout(p.ctx, internal.UpstreamTimeout)
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

	// Save to Redis with completed status
	redisCtx, redisCancel := context.WithTimeout(p.ctx, internal.RedisTimeout)
	defer redisCancel()

	return p.store.SaveResponse(redisCtx, &internal.Response{
		Status:    "completed",
		RequestID: m.RequestID,
		Result:    body,
	}, internal.ResultTTL)
}

func (p *Processor) Close() {
	p.cancel() // Cancel parent context
}
