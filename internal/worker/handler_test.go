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
	kafka "github.com/segmentio/kafka-go"
)

// buildMsg wraps raw JSON into a kafka.Message whose Value is a KafkaMessage JSON envelope.
func buildMsg(t *testing.T, raw string) kafka.Message {
	t.Helper()
	kmJSON, err := json.Marshal(internal.KafkaMessage{
		RequestID:  "test-req-1",
		Raw:        json.RawMessage(raw),
		ReceivedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("buildMsg: %v", err)
	}
	return kafka.Message{
		Key:   []byte("test-req-1"),
		Value: kmJSON,
	}
}

func newTestProcessor(t *testing.T, serverURL string) *Processor {
	t.Helper()
	p := NewProcessor()
	p.upstreamURL = serverURL
	return p
}

func TestProcessBatch_InvalidJSON(t *testing.T) {
	p := NewProcessor()
	defer p.Close()

	_, err := p.ProcessBatch(map[string]kafka.Message{
		"bad": {Key: []byte("bad"), Value: []byte("not-json")},
	})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestProcessBatch_SuccessFirstAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"test-req-1","jsonrpc":"2.0","result":"ok"}]`)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	msg := buildMsg(t, `{"method":"eth_blockNumber","id":1}`)
	results, err := p.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(results["test-req-1"]), `"result":"ok"`) {
		t.Fatalf("unexpected body: %s", results["test-req-1"])
	}
}

func TestProcessBatch_RetryOn5xx_ThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < int32(internal.UpstreamMaxRetries) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"test-req-1","jsonrpc":"2.0","result":"ok"}]`)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	msg := buildMsg(t, `{"jsonrpc":"2.0","id":1}`)
	results, err := p.ProcessBatch(map[string]kafka.Message{"test-req-1": msg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(results["test-req-1"]), `"result":"ok"`) {
		t.Fatalf("unexpected body: %s", results["test-req-1"])
	}
	if int(calls.Load()) != int(internal.UpstreamMaxRetries) {
		t.Fatalf("expected %d calls, got %d", internal.UpstreamMaxRetries, calls.Load())
	}
}

func TestProcessBatch_NoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	_, err := p.ProcessBatch(map[string]kafka.Message{"test-req-1": buildMsg(t, `{"jsonrpc":"2.0","id":1}`)})
	if err == nil {
		t.Fatal("expected error for 4xx response")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 call (no retry on 4xx), got %d", calls.Load())
	}
}

func TestProcessBatch_MaxRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	_, err := p.ProcessBatch(map[string]kafka.Message{"test-req-1": buildMsg(t, `{"id":1}`)})
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if calls.Load() != int32(internal.UpstreamMaxRetries) {
		t.Fatalf("expected %d calls, got %d", internal.UpstreamMaxRetries, calls.Load())
	}
}

func TestProcessBatch_ContextCancelledDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)

	done := make(chan error, 1)
	go func() {
		_, err := p.ProcessBatch(map[string]kafka.Message{"test-req-1": buildMsg(t, `{"id":1}`)})
		done <- err
	}()

	time.Sleep(30 * time.Millisecond)
	p.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessBatch did not return after context cancellation")
	}
}

func TestProcessBatch_LargeResponseTruncated(t *testing.T) {
	largeItem := strings.Repeat("x", int(internal.MaxUpstreamBatchResponseSize)+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, largeItem)
	}))
	defer srv.Close()

	p := newTestProcessor(t, srv.URL)
	defer p.Close()

	_, err := p.ProcessBatch(map[string]kafka.Message{"test-req-1": buildMsg(t, `{"id":1}`)})
	if err == nil {
		t.Fatal("expected error for oversized / malformed batch response")
	}
}

// TestProcessBatch_RealUpstream hits the real upstream and verifies a valid batch
// JSON-RPC response is returned.
func TestProcessBatch_RealUpstream(t *testing.T) {
	p := NewProcessor() // uses internal.UpstreamURL
	defer p.Close()

	msgs := map[string]kafka.Message{
		"req-chainid":     buildMsgWithID(t, "req-chainid", `{"jsonrpc":"2.0","method":"eth_chainId","params":[]}`),
		"req-blocknumber": buildMsgWithID(t, "req-blocknumber", `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}`),
	}

	results, err := p.ProcessBatch(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for id := range msgs {
		raw, ok := results[id]
		if !ok {
			t.Errorf("missing response for %s", id)
			continue
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Errorf("[%s] invalid JSON: %v", id, err)
			continue
		}
		if resp["result"] == nil {
			t.Errorf("[%s] expected result field, got: %s", id, raw)
		}
		t.Logf("[%s] result: %v", id, resp["result"])
	}
}

// buildMsgWithID is like buildMsg but allows specifying an explicit request ID.
func buildMsgWithID(t *testing.T, requestID, raw string) kafka.Message {
	t.Helper()
	kmJSON, err := json.Marshal(internal.KafkaMessage{
		RequestID:  requestID,
		Raw:        json.RawMessage(raw),
		ReceivedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("buildMsgWithID: %v", err)
	}
	return kafka.Message{Key: []byte(requestID), Value: kmJSON}
}
