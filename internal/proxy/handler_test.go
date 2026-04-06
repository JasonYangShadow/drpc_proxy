package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"drpc_proxy.com/internal"
)

// --- fakes ---

type fakeStore struct {
	mu      sync.Mutex
	data    map[string]*internal.Response
	saveErr error
	getErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: make(map[string]*internal.Response)}
}

func (s *fakeStore) SaveResponse(_ context.Context, resp *internal.Response, _ time.Duration) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *resp
	s.data[resp.RequestID] = &cp
	return nil
}

func (s *fakeStore) GetResponse(_ context.Context, requestID string) (*internal.Response, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[requestID]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *r
	return &cp, nil
}

type fakeSender struct {
	sendErr error
}

func (f *fakeSender) SendWithContext(_ context.Context, _ string, _ []byte) error {
	return f.sendErr
}

// buildHandler creates a Handler with 1 concurrency slot and 1 Kafka worker.
func buildHandler(store responseStorer, sender messageSender) *Handler {
	return NewHandler(sender, store, 1, 1)
}

// --- HandleRPC tests ---

func TestHandleRPC_EmptyBody(t *testing.T) {
	h := buildHandler(newFakeStore(), &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(""))
	w := httptest.NewRecorder()
	h.HandleRPC(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	assertContentTypeJSON(t, w)
}

func TestHandleRPC_BodyTooLarge(t *testing.T) {
	h := buildHandler(newFakeStore(), &fakeSender{})
	defer h.Close()

	body := strings.Repeat("x", internal.MaxUpstreamRequestSize+10)
	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleRPC(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
	assertContentTypeJSON(t, w)
}

func TestHandleRPC_ServerOverloaded(t *testing.T) {
	// Use 0 maxConcurrent slots would panic; instead pre-fill the semaphore manually.
	h := buildHandler(newFakeStore(), &fakeSender{})
	defer h.Close()

	// Grab the only semaphore slot so the next request sees it full.
	h.semaphore <- struct{}{}

	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"eth_call"}`))
	w := httptest.NewRecorder()
	h.HandleRPC(w, req)
	<-h.semaphore // release

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	assertContentTypeJSON(t, w)
}

func TestHandleRPC_RedisFailure(t *testing.T) {
	store := newFakeStore()
	store.saveErr = errors.New("redis down")

	h := buildHandler(store, &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"eth_call"}`))
	w := httptest.NewRecorder()
	h.HandleRPC(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	assertContentTypeJSON(t, w)
}

func TestHandleRPC_QueueFull_UpdatesRedisFailed(t *testing.T) {
	store := newFakeStore()

	// Construct Handler directly without starting workers so the channel stays full.
	h := &Handler{
		producer:  &fakeSender{},
		store:     store,
		semaphore: make(chan struct{}, 1),
		kafkaCh:   make(chan *internal.KafkaMessage, 2),
	}
	defer h.Close()

	// Fill the channel to capacity — no workers will drain it.
	h.kafkaCh <- &internal.KafkaMessage{}
	h.kafkaCh <- &internal.KafkaMessage{}

	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"eth_call"}`))
	w := httptest.NewRecorder()
	h.HandleRPC(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (body: %s)", w.Code, w.Body.String())
	}
	assertContentTypeJSON(t, w)

	// Check that Redis was updated to "failed" for this request.
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, resp := range store.data {
		if resp.Status == "failed" {
			return // found the expected failed entry
		}
	}
	t.Fatal("expected a 'failed' response in Redis after queue full")
}

func TestHandleRPC_Success_Returns200(t *testing.T) {
	store := newFakeStore()
	h := buildHandler(store, &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"eth_call"}`))
	w := httptest.NewRecorder()
	h.HandleRPC(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (accepted), got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp internal.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "accepted" {
		t.Fatalf("expected status=accepted, got %s", resp.Status)
	}
	if resp.RequestID == "" {
		t.Fatal("expected RequestID to be set")
	}
}

// --- HandleResult tests ---

func TestHandleResult_MissingParam(t *testing.T) {
	h := buildHandler(newFakeStore(), &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodGet, "/result", nil)
	w := httptest.NewRecorder()
	h.HandleResult(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleResult_NotFound(t *testing.T) {
	store := newFakeStore()
	store.getErr = errors.New("not found")
	h := buildHandler(store, &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodGet, "/result?request_id=missing", nil)
	w := httptest.NewRecorder()
	h.HandleResult(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleResult_Pending(t *testing.T) {
	store := newFakeStore()
	_ = store.SaveResponse(context.Background(), &internal.Response{
		Status:    "pending",
		RequestID: "req-123",
	}, time.Minute)

	h := buildHandler(store, &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodGet, "/result?request_id=req-123", nil)
	w := httptest.NewRecorder()
	h.HandleResult(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestHandleResult_Completed(t *testing.T) {
	store := newFakeStore()
	_ = store.SaveResponse(context.Background(), &internal.Response{
		Status:    "completed",
		RequestID: "req-456",
		Result:    json.RawMessage(`{"block":"0x1"}`),
	}, time.Minute)

	h := buildHandler(store, &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodGet, "/result?request_id=req-456", nil)
	w := httptest.NewRecorder()
	h.HandleResult(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp internal.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("expected completed, got %s", resp.Status)
	}
}

func TestHandleResult_Failed(t *testing.T) {
	store := newFakeStore()
	_ = store.SaveResponse(context.Background(), &internal.Response{
		Status:    "failed",
		RequestID: "req-789",
		Error:     "upstream error",
	}, time.Minute)

	h := buildHandler(store, &fakeSender{})
	defer h.Close()

	req := httptest.NewRequest(http.MethodGet, "/result?request_id=req-789", nil)
	w := httptest.NewRecorder()
	h.HandleResult(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func assertContentTypeJSON(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected Content-Type: application/json, got %q", ct)
	}
}
