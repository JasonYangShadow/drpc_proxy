package worker

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"drpc_proxy.com/internal"
)

// buildMsg wraps raw JSON into a KafkaMessage payload.
func buildMsg(t *testing.T, raw string) []byte {
	t.Helper()
	km := internal.KafkaMessage{
		RequestID:  "test-req-1",
		Raw:        json.RawMessage(raw),
		ReceivedAt: time.Now().Unix(),
	}
	b, err := json.Marshal(km)
	if err != nil {
		t.Fatalf("buildMsg: %v", err)
	}
	return b
}

func newTestProcessor(t *testing.T, serverURL string) *Processor {
	t.Helper()
	p := NewProcessor()
	p.upstreamURL = serverURL
	return p
}

func TestProcess_InvalidJSON(t *testing.T) {
	p := NewProcessor()
	defer p.Close()

	_, err := p.Process([]byte("not-json"))
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestProcess_SuccessFirstAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"result":"ok"}`)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	got, err := p.Process(buildMsg(t, `{"method":"eth_blockNumber"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `{"result":"ok"}` {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestProcess_RetryOn5xx_ThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < int32(internal.UpstreamMaxRetries) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"result":"ok"}`)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	got, err := p.Process(buildMsg(t, `{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `{"result":"ok"}` {
		t.Fatalf("unexpected body: %s", got)
	}
	if int(calls.Load()) != int(internal.UpstreamMaxRetries) {
		t.Fatalf("expected %d calls, got %d", internal.UpstreamMaxRetries, calls.Load())
	}
}

func TestProcess_NoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	body, err := p.Process(buildMsg(t, `{"jsonrpc":"2.0"}`))
	if err == nil {
		t.Fatal("expected error for 4xx response")
	}
	upErr, ok := err.(*upstreamError)
	if !ok {
		t.Fatalf("expected *upstreamError, got %T: %v", err, err)
	}
	if upErr.statusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", upErr.statusCode)
	}
	if !strings.Contains(string(body), "bad request") {
		t.Fatalf("expected 4xx body to be returned, got: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 call (no retry on 4xx), got %d", calls.Load())
	}
}

func TestProcess_MaxRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	_, err := p.Process(buildMsg(t, `{}`))
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if calls.Load() != int32(internal.UpstreamMaxRetries) {
		t.Fatalf("expected %d calls, got %d", internal.UpstreamMaxRetries, calls.Load())
	}
}

func TestProcess_ContextCancelledDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)

	done := make(chan error, 1)
	go func() {
		_, err := p.Process(buildMsg(t, `{}`))
		done <- err
	}()

	// Given attempt 1 fires quickly, cancel mid-backoff.
	time.Sleep(30 * time.Millisecond)
	p.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not return after context cancellation")
	}
}

func TestProcess_LargeResponseTruncated(t *testing.T) {
	largeBody := strings.Repeat("x", int(internal.MaxUpstreamResponseSize)+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, largeBody)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	got, err := p.Process(buildMsg(t, `{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > int(internal.MaxUpstreamResponseSize) {
		t.Fatalf("response not truncated: got %d bytes", len(got))
	}
}
