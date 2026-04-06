package worker

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"drpc_proxy.com/internal"
	"github.com/bytedance/sonic"
	kafka "github.com/segmentio/kafka-go"
)

// MockProcessor is a drop-in replacement for Processor that simulates upstream
// latency without making real network calls. It is intended for mock tests and
// load tests.
//
// Behaviour:
//   - Latency:             uniform random between MinLatency and MaxLatency (default 100–200 ms)
//   - NonRetryableFailRate: probability of a non-retryable 4xx error (default 0.0001)
//   - RetryableFailRate:    probability of a retryable 429/5xx error per attempt (default 0)
//   - Response:            a minimal JSON-RPC result echoing the request_id
type MockProcessor struct {
	ctx    context.Context
	cancel context.CancelFunc

	MinLatency           time.Duration // default: 100ms
	MaxLatency           time.Duration // default: 200ms
	NonRetryableFailRate float64       // default: 0.0001 — simulates 4xx (non-retryable)
	RetryableFailRate    float64       // default: 0      — simulates 429/5xx (retryable)

	// Deprecated: use NonRetryableFailRate. Kept for backward compatibility.
	FailRate float64
}

// Compile-time assertion that MockProcessor satisfies MessageHandler.
var _ MessageHandler = (*MockProcessor)(nil)

func NewMockProcessor() *MockProcessor {
	ctx, cancel := context.WithCancel(context.Background())
	return &MockProcessor{
		ctx:                  ctx,
		cancel:               cancel,
		MinLatency:           100 * time.Millisecond,
		MaxLatency:           200 * time.Millisecond,
		NonRetryableFailRate: 0.0001,
		RetryableFailRate:    0,
	}
}

func (m *MockProcessor) Close() {
	m.cancel()
}

// ProcessBatch mirrors the real Processor pipeline:
//  1. Unmarshal each KafkaMessage, parse raw RPC payload, inject request_id as "id".
//  2. Simulate a batch round-trip with one shared latency sleep per attempt.
//  3. Non-retryable failures (NonRetryableFailRate / FailRate) stop immediately.
//  4. Retryable failures (RetryableFailRate) back off and retry up to UpstreamMaxRetries.
func (m *MockProcessor) ProcessBatch(msgs map[string]kafka.Message) (map[string][]byte, error) {
	// Step 1: parse all messages, mirroring the real handler's preparation loop.
	type parsedItem struct {
		requestID string
		rpcReq    map[string]interface{}
	}
	items := make([]parsedItem, 0, len(msgs))
	for requestID, kafkaMsg := range msgs {
		var km internal.KafkaMessage
		if err := sonic.Unmarshal(kafkaMsg.Value, &km); err != nil {
			return nil, err
		}
		var rpcReq map[string]interface{}
		if err := sonic.Unmarshal(km.Raw, &rpcReq); err != nil {
			return nil, err
		}
		rpcReq["id"] = requestID
		items = append(items, parsedItem{requestID: requestID, rpcReq: rpcReq})
	}

	if len(items) == 0 {
		return nil, nil
	}

	// Resolve effective non-retryable rate (FailRate is the legacy field).
	nonRetryableRate := m.NonRetryableFailRate
	if m.FailRate != 0 {
		nonRetryableRate = m.FailRate
	}

	// Step 2: simulate batch round-trip with retry loop, mirroring the real handler.
	var batchErr error
	for attempt := 1; attempt <= internal.UpstreamMaxRetries; attempt++ {
		// Simulate one network round-trip latency.
		select {
		case <-time.After(m.randomLatency()):
		case <-m.ctx.Done():
			return nil, fmt.Errorf("mock processor shutting down: %w", m.ctx.Err())
		}

		// Non-retryable failure (simulates 4xx except 429): stop immediately.
		if rand.Float64() < nonRetryableRate {
			return nil, &upstreamError{statusCode: 400, body: []byte("mock non-retryable error (simulated)")}
		}

		// Retryable failure (simulates 429 / 5xx): backoff and retry.
		if rand.Float64() < m.RetryableFailRate {
			batchErr = fmt.Errorf("mock retryable error (simulated 429/5xx)")
			if attempt == internal.UpstreamMaxRetries {
				break
			}
			backoff := internal.UpstreamRetryInitialBackoff * (1 << (attempt - 1))
			if backoff > internal.UpstreamRetryMaxBackoff {
				backoff = internal.UpstreamRetryMaxBackoff
			}
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
				t.Stop()
				continue
			case <-m.ctx.Done():
				t.Stop()
				return nil, fmt.Errorf("context cancelled during retry backoff: %w", m.ctx.Err())
			}
		}

		batchErr = nil
		break
	}

	if batchErr != nil {
		return nil, batchErr
	}

	// Step 3: build a minimal JSON-RPC response for each item, echoing request_id as "id".
	results := make(map[string][]byte, len(items))
	for _, it := range items {
		resp, err := sonic.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      it.requestID,
			"result":  fmt.Sprintf("0x%x", time.Now().UnixNano()&0xffff),
		})
		if err != nil {
			return nil, fmt.Errorf("mock: failed to build response: %w", err)
		}
		results[it.requestID] = resp
	}
	return results, nil
}

// randomLatency returns a uniform random duration between MinLatency and MaxLatency.
func (m *MockProcessor) randomLatency() time.Duration {
	spread := int64(m.MaxLatency - m.MinLatency)
	if spread <= 0 {
		return m.MinLatency
	}
	return m.MinLatency + time.Duration(rand.Int63n(spread))
}
