package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"drpc_proxy.com/internal"
	"github.com/bytedance/sonic"
)

// MockProcessor is a drop-in replacement for Processor that simulates upstream
// latency without making real network calls. It is intended for mock tests and
// load tests.
//
// Behaviour:
//   - Latency:      uniform random between MockMinLatency and MockMaxLatency (default 100–200 ms)
//   - Failure rate: MockFailRate probability of returning a simulated 500 error (default 0.01%)
//   - Response:     a minimal JSON-RPC result echoing the request id
type MockProcessor struct {
	ctx    context.Context
	cancel context.CancelFunc

	MinLatency time.Duration // default: 100ms
	MaxLatency time.Duration // default: 200ms
	FailRate   float64       // default: 0.0001 (0.01%)
}

// Compile-time assertion that MockProcessor satisfies MessageHandler.
var _ MessageHandler = (*MockProcessor)(nil)

func NewMockProcessor() *MockProcessor {
	ctx, cancel := context.WithCancel(context.Background())
	return &MockProcessor{
		ctx:        ctx,
		cancel:     cancel,
		MinLatency: 100 * time.Millisecond,
		MaxLatency: 200 * time.Millisecond,
		FailRate:   0.0001,
	}
}

func (m *MockProcessor) Process(msg []byte) ([]byte, error) {
	var km internal.KafkaMessage
	if err := sonic.Unmarshal(msg, &km); err != nil {
		return nil, err
	}

	// Simulate upstream latency — interruptible on Close().
	latency := m.randomLatency()
	select {
	case <-time.After(latency):
	case <-m.ctx.Done():
		return nil, fmt.Errorf("mock processor shutting down: %w", m.ctx.Err())
	}

	// Simulate rare upstream failure.
	if rand.Float64() < m.FailRate {
		return nil, fmt.Errorf("mock upstream error (simulated)")
	}

	// Build a minimal JSON-RPC success response.
	resp, err := m.buildResponse(km)
	if err != nil {
		return nil, fmt.Errorf("mock: failed to build response: %w", err)
	}
	return resp, nil
}

func (m *MockProcessor) Close() {
	m.cancel()
}

// randomLatency returns a uniform random duration between MinLatency and MaxLatency.
func (m *MockProcessor) randomLatency() time.Duration {
	spread := int64(m.MaxLatency - m.MinLatency)
	if spread <= 0 {
		return m.MinLatency
	}
	return m.MinLatency + time.Duration(rand.Int63n(spread))
}

// buildResponse builds a minimal JSON-RPC 2.0 result response that echoes the
// request id from the original KafkaMessage payload.
func (m *MockProcessor) buildResponse(km internal.KafkaMessage) ([]byte, error) {
	// Best-effort: try to extract "id" from the raw RPC payload.
	var rpc struct {
		ID interface{} `json:"id"`
	}
	_ = json.Unmarshal(km.Raw, &rpc)

	result := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      rpc.ID,
		"result":  "0x" + fmt.Sprintf("%x", time.Now().UnixNano()&0xffff),
	}
	return sonic.Marshal(result)
}
