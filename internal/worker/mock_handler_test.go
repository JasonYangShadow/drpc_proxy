package worker

import (
	"encoding/json"
	"testing"
	"time"

	"drpc_proxy.com/internal"
	kafka "github.com/segmentio/kafka-go"
)

// Verify interface compliance at compile time.
var (
	_ MessageHandler = (*MockProcessor)(nil)
	_ MessageHandler = (*Processor)(nil)
)

func TestMockProcessor_SuccessLatency(t *testing.T) {
	m := NewMockProcessor()
	defer m.Close()
	m.NonRetryableFailRate = 0

	msg := buildMsg(t, `{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}`)

	start := time.Now()
	results, err := m.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < m.MinLatency {
		t.Fatalf("latency %v below MinLatency %v", elapsed, m.MinLatency)
	}
	if elapsed > m.MaxLatency+50*time.Millisecond {
		t.Fatalf("latency %v exceeds MaxLatency %v (+50ms tolerance)", elapsed, m.MaxLatency)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(results["test-req-1"], &result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if result["jsonrpc"] != "2.0" {
		t.Fatalf("expected jsonrpc=2.0, got %v", result["jsonrpc"])
	}
	if result["result"] == nil {
		t.Fatal("expected result field to be present")
	}
}

func TestMockProcessor_EchosRequestID(t *testing.T) {
	m := NewMockProcessor()
	m.NonRetryableFailRate = 0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","method":"eth_call","id":42}`)
	results, err := m.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(results["test-req-1"], &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// The response id is the request_id (Kafka key), not the original RPC id.
	if result["id"] != "test-req-1" {
		t.Fatalf("expected id=test-req-1, got %v", result["id"])
	}
}

func TestMockProcessor_InvalidJSON(t *testing.T) {
	m := NewMockProcessor()
	defer m.Close()

	_, err := m.ProcessBatch(map[string]kafka.Message{"bad": {Key: []byte("bad"), Value: []byte("not-json")}})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMockProcessor_AlwaysFails(t *testing.T) {
	m := NewMockProcessor()
	m.NonRetryableFailRate = 1.0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","id":1}`)
	_, err := m.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
	if err == nil {
		t.Fatal("expected failure with NonRetryableFailRate=1.0")
	}
}

func TestMockProcessor_NeverFails(t *testing.T) {
	m := NewMockProcessor()
	m.NonRetryableFailRate = 0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","id":1}`)
	for i := 0; i < 100; i++ {
		_, err := m.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
	}
}

func TestMockProcessor_Close_StopsProcessing(t *testing.T) {
	m := NewMockProcessor()
	m.NonRetryableFailRate = 0
	m.MinLatency = 500 * time.Millisecond
	m.MaxLatency = 1 * time.Second

	msg := buildMsg(t, `{}`)
	done := make(chan error, 1)
	go func() {
		_, err := m.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	m.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after Close()")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MockProcessor did not return after Close()")
	}
}

func TestMockProcessor_StatisticalFailRate(t *testing.T) {
	const iterations = 100_000
	m := NewMockProcessor()
	m.NonRetryableFailRate = 0.0001 // 0.01%
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{}`)
	var failures int
	for i := 0; i < iterations; i++ {
		_, err := m.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
		if err != nil {
			failures++
		}
	}

	// With FailRate=0.0001 and 100k iterations, expected failures ≈ 10.
	// Accept anything in [0, 60] — this is a statistical smoke test.
	rate := float64(failures) / float64(iterations)
	t.Logf("failure rate: %.5f%% (%d/%d)", rate*100, failures, iterations)
	if failures > 60 {
		t.Fatalf("failure rate %.5f%% is too high (expected ~0.01%%)", rate*100)
	}
}

// buildMsg is defined in handler_test.go (same package).

// TestMockResponseShape verifies the response contains all required JSON-RPC fields.
func TestMockResponseShape(t *testing.T) {
	m := NewMockProcessor()
	m.NonRetryableFailRate = 0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	km := internal.KafkaMessage{
		RequestID:  "shape-test",
		Raw:        []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","id":"xyz"}`),
		ReceivedAt: time.Now().Unix(),
	}
	b, _ := json.Marshal(km)

	results, err := m.ProcessBatch(map[string]kafka.Message{"shape-test": {Key: []byte("shape-test"), Value: b}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(results["shape-test"], &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, field := range []string{"jsonrpc", "id", "result"} {
		if result[field] == nil {
			t.Errorf("missing field %q in response", field)
		}
	}
	// The real handler echoes request_id (Kafka key) as the response id.
	// "shape-test" is the RequestID set in the KafkaMessage envelope.
	if result["id"] != "shape-test" {
		t.Fatalf("expected id=shape-test, got %v", result["id"])
	}
}
