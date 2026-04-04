package worker

import (
	"encoding/json"
	"testing"
	"time"

	"drpc_proxy.com/internal"
)

// Verify interface compliance at compile time.
var (
	_ MessageHandler = (*MockProcessor)(nil)
	_ MessageHandler = (*Processor)(nil)
)

func TestMockProcessor_SuccessLatency(t *testing.T) {
	m := NewMockProcessor()
	defer m.Close()
	// Zero out failure rate so this test is deterministic.
	m.FailRate = 0

	msg := buildMsg(t, `{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}`)

	start := time.Now()
	resp, err := m.Process(msg)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < m.MinLatency {
		t.Fatalf("latency %v below MinLatency %v", elapsed, m.MinLatency)
	}
	if elapsed > m.MaxLatency+50*time.Millisecond { // +50ms tolerance for scheduler jitter
		t.Fatalf("latency %v exceeds MaxLatency %v (+50ms tolerance)", elapsed, m.MaxLatency)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp, &result); err != nil {
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
	m.FailRate = 0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","method":"eth_call","id":42}`)
	resp, err := m.Process(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// JSON numbers unmarshal as float64.
	if result["id"] != float64(42) {
		t.Fatalf("expected id=42, got %v", result["id"])
	}
}

func TestMockProcessor_InvalidJSON(t *testing.T) {
	m := NewMockProcessor()
	defer m.Close()

	_, err := m.Process([]byte("not-json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMockProcessor_AlwaysFails(t *testing.T) {
	m := NewMockProcessor()
	m.FailRate = 1.0 // guaranteed failure
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","id":1}`)
	_, err := m.Process(msg)
	if err == nil {
		t.Fatal("expected failure with FailRate=1.0")
	}
}

func TestMockProcessor_NeverFails(t *testing.T) {
	m := NewMockProcessor()
	m.FailRate = 0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","id":1}`)
	for i := 0; i < 100; i++ {
		if _, err := m.Process(msg); err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
	}
}

func TestMockProcessor_Close_StopsProcessing(t *testing.T) {
	m := NewMockProcessor()
	m.FailRate = 0
	m.MinLatency = 500 * time.Millisecond
	m.MaxLatency = 1 * time.Second

	msg := buildMsg(t, `{}`)
	done := make(chan error, 1)
	go func() {
		_, err := m.Process(msg)
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
	m.FailRate = 0.0001 // 0.01%
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	msg := buildMsg(t, `{}`)
	var failures int
	for i := 0; i < iterations; i++ {
		_, err := m.Process(msg)
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
	m.FailRate = 0
	m.MinLatency = 0
	m.MaxLatency = 0
	defer m.Close()

	km := internal.KafkaMessage{
		RequestID:  "shape-test",
		Raw:        []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","id":"xyz"}`),
		ReceivedAt: time.Now().Unix(),
	}
	b, _ := json.Marshal(km)

	resp, err := m.Process(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, field := range []string{"jsonrpc", "id", "result"} {
		if result[field] == nil {
			t.Errorf("missing field %q in response", field)
		}
	}
	if result["id"] != "xyz" {
		t.Fatalf("expected id=xyz, got %v", result["id"])
	}
}
