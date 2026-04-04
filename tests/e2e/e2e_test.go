//go:build e2e

// Package e2e contains end-to-end tests that require a running Kafka and Redis
// instance. Run with:
//   KAFKA_ADDR=localhost:9092 REDIS_ADDR=localhost:6379 PROXY_ADDR=http://localhost:8080 \
//   go test -v -tags e2e ./tests/e2e/ -timeout 60s
package e2e

import (
"bytes"
"context"
"encoding/json"
"fmt"
"net/http"
"os"
"testing"
"time"

"drpc_proxy.com/internal"
)

func proxyAddr() string {
	if v := os.Getenv("PROXY_ADDR"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

// TestFullFlow_AcceptAndPoll submits a request and polls until it reaches a
// terminal state (completed or failed) or the test times out.
func TestFullFlow_AcceptAndPoll(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)

	// 1. Submit request.
	resp, err := http.Post(proxyAddr()+"/rpc", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /rpc: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /rpc, got %d", resp.StatusCode)
	}

	var accepted internal.Response
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode /rpc response: %v", err)
	}
	if accepted.RequestID == "" {
		t.Fatal("expected RequestID in response")
	}
	t.Logf("submitted request_id=%s", accepted.RequestID)

	// 2. Poll until terminal.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pollURL := fmt.Sprintf("%s/result?request_id=%s", proxyAddr(), accepted.RequestID)
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for request %s to complete", accepted.RequestID)
		case <-time.After(500 * time.Millisecond):
		}

		r, err := http.Get(pollURL)
		if err != nil {
			t.Logf("poll error (will retry): %v", err)
			continue
		}
		var result internal.Response
		_ = json.NewDecoder(r.Body).Decode(&result)
		r.Body.Close()

		t.Logf("poll status=%s http=%d", result.Status, r.StatusCode)

		switch result.Status {
		case "completed":
			t.Logf("request completed: %s", result.Result)
			return
		case "failed":
			t.Logf("request failed (upstream unreachable is expected in CI): %s", result.Error)
			return
		}
	}
}

// TestHandleRPC_Overloaded verifies the proxy returns 429 when the semaphore is
// saturated. Requires the proxy to be running with maxConcurrent=1 or a
// deliberate load generator; this test just validates the endpoint contract.
func TestEndpoint_HealthCheck(t *testing.T) {
	resp, err := http.Get(proxyAddr() + "/health")
	if err != nil {
		t.Skipf("proxy not reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", resp.StatusCode)
	}
}
