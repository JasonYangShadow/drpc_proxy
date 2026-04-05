//go:build e2e

// Package e2e contains end-to-end tests that require a running LocalStack
// deployment (proxy + worker via ECS). Run with:
//
//	PROXY_ADDR=http://<proxy-ip>:8545 go test -v -tags e2e ./tests/e2e/ -timeout 120s
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"drpc_proxy.com/internal"
)

func proxyAddr() string {
	if v := os.Getenv("PROXY_ADDR"); v != "" {
		return v
	}
	return "http://localhost:8545"
}

// TestMain performs a single reachability check before running any test.
// If the proxy is not reachable the whole suite is skipped with a clear message.
func TestMain(m *testing.M) {
	resp, err := http.Get(proxyAddr() + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("SKIP: proxy not reachable at %s (set PROXY_ADDR to override)\n", proxyAddr())
		os.Exit(0)
	}
	resp.Body.Close()
	os.Exit(m.Run())
}

// pollResult polls GET /result?request_id=<id> until status is terminal or ctx expires.
// Returns the final Response and the last HTTP status code seen.
func pollResult(t *testing.T, ctx context.Context, requestID string) (*internal.Response, int) {
	t.Helper()
	pollURL := fmt.Sprintf("%s/result?request_id=%s", proxyAddr(), requestID)
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for request %s to complete", requestID)
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
		case "completed", "failed":
			return &result, r.StatusCode
		}
	}
}

// submitRPC posts a JSON-RPC payload to /rpc and returns the accepted Response.
func submitRPC(t *testing.T, payload []byte) *internal.Response {
	t.Helper()
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
		t.Fatal("expected non-empty request_id in /rpc response")
	}
	return &accepted
}

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

// TestFullFlow_AcceptAndPoll submits a real eth_blockNumber request and polls
// until the result reaches a terminal state.
func TestFullFlow_AcceptAndPoll(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	accepted := submitRPC(t, payload)
	t.Logf("submitted request_id=%s", accepted.RequestID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, _ := pollResult(t, ctx, accepted.RequestID)
	t.Logf("final status=%s result=%s error=%s", result.Status, result.Result, result.Error)
}

// TestFullFlow_ConcurrentRequests submits N requests concurrently and verifies
// each one independently reaches a terminal state.
func TestFullFlow_ConcurrentRequests(t *testing.T) {
	const n = 5
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)

	ids := make([]string, n)
	for i := 0; i < n; i++ {
		accepted := submitRPC(t, payload)
		ids[i] = accepted.RequestID
		t.Logf("[%d] submitted request_id=%s", i, accepted.RequestID)
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			result, _ := pollResult(t, ctx, id)
			t.Logf("request_id=%s final status=%s", id, result.Status)
		}()
	}
	wg.Wait()
}

// TestRPC_EmptyBody verifies that an empty POST body returns 400.
func TestRPC_EmptyBody(t *testing.T) {
	resp, err := http.Post(proxyAddr()+"/rpc", "application/json", bytes.NewReader([]byte{}))
	if err != nil {
		t.Skipf("proxy not reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d", resp.StatusCode)
	}
}

// TestRPC_OversizedBody verifies that a body beyond MaxUpstreamRequestSize returns 413.
func TestRPC_OversizedBody(t *testing.T) {
	// MaxUpstreamRequestSize = 128KB; send 130KB
	oversized := bytes.Repeat([]byte("x"), 130*1024)
	resp, err := http.Post(proxyAddr()+"/rpc", "application/json", bytes.NewReader(oversized))
	if err != nil {
		t.Skipf("proxy not reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d", resp.StatusCode)
	}
}

// TestResult_MissingRequestID verifies GET /result without request_id returns 400.
func TestResult_MissingRequestID(t *testing.T) {
	resp, err := http.Get(proxyAddr() + "/result")
	if err != nil {
		t.Skipf("proxy not reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing request_id, got %d", resp.StatusCode)
	}
}

// TestResult_UnknownRequestID verifies GET /result for a nonexistent ID returns 404.
func TestResult_UnknownRequestID(t *testing.T) {
	resp, err := http.Get(proxyAddr() + "/result?request_id=00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Skipf("proxy not reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown request_id, got %d", resp.StatusCode)
	}
}

// TestResult_PendingThenTerminal verifies that immediately after submission the
// status is pending/queued (202), then eventually becomes terminal.
func TestResult_PendingThenTerminal(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}`)
	accepted := submitRPC(t, payload)

	// Poll once immediately — should be pending or queued (202).
	pollURL := fmt.Sprintf("%s/result?request_id=%s", proxyAddr(), accepted.RequestID)
	r, err := http.Get(pollURL)
	if err != nil {
		t.Skipf("proxy not reachable: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted && r.StatusCode != http.StatusOK {
		t.Fatalf("expected 202 or 200 immediately after submission, got %d", r.StatusCode)
	}

	// Then wait for terminal.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, _ := pollResult(t, ctx, accepted.RequestID)
	t.Logf("final status=%s", result.Status)
}

// TestRPC_DifferentMethods verifies that a range of JSON-RPC methods are all
// accepted and reach a terminal state.
func TestRPC_DifferentMethods(t *testing.T) {
	methods := []string{
		`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`,
		`{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}`,
		`{"jsonrpc":"2.0","method":"eth_gasPrice","params":[],"id":3}`,
	}

	for _, m := range methods {
		m := m
		method := m[strings.Index(m, `"method":"`)+10 : strings.Index(m, `","params`)]
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			accepted := submitRPC(t, []byte(m))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, _ := pollResult(t, ctx, accepted.RequestID)
			t.Logf("method=%s final_status=%s", method, result.Status)
		})
	}
}
