package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/redis"
	"github.com/bytedance/sonic"
)

const (
	upstreamTimeout = 30 * time.Second
	redisTimeout    = 2 * time.Second
	resultTTL       = 1 * time.Hour
)

type Processor struct {
	store *redis.Store
}

func NewProcessor(store *redis.Store) *Processor {
	return &Processor{store: store}
}

func (p *Processor) Process(msg []byte) error {
	var m struct {
		RequestID  string              `json:"request_id"`
		RPCRequest internal.RPCRequest `json:"rpc_request"`
	}

	if err := sonic.Unmarshal(msg, &m); err != nil {
		log.Printf("Failed to unmarshal message: %v", err)
		return err
	}

	// 序列化 RPC 请求
	rpcPayload, err := sonic.Marshal(m.RPCRequest)
	if err != nil {
		log.Printf("Failed to marshal RPC request for %s: %v", m.RequestID, err)
		return err
	}

	// 调用上游 RPC with timeout
	upstreamCtx, upstreamCancel := context.WithTimeout(context.Background(), upstreamTimeout)
	defer upstreamCancel()

	req, err := http.NewRequestWithContext(upstreamCtx, "POST", "https://polygon-amoy.drpc.org", bytes.NewReader(rpcPayload))
	if err != nil {
		log.Printf("Failed to create request for %s: %v", m.RequestID, err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Upstream error for %s: %v", m.RequestID, err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body for %s: %v", m.RequestID, err)
		return err
	}

	// 写入 Redis with timeout
	redisCtx, redisCancel := context.WithTimeout(context.Background(), redisTimeout)
	defer redisCancel()

	return p.store.SaveWithContext(redisCtx, m.RequestID, json.RawMessage(body), resultTTL)
}
