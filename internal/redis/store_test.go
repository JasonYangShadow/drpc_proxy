package redis

import (
"context"
"encoding/json"
"errors"
"testing"
"time"

"drpc_proxy.com/internal"
"github.com/redis/go-redis/v9"
)

// --- fake redis client ---

type entry struct {
	value []byte
	ttl   time.Duration
}

type fakeRedisClient struct {
	data     map[string]entry
	setErr   error
	getErr   error
	// closeDelay simulates a slow Close() for the timeout test.
	closeDelay time.Duration
	closed     bool
}

func newFakeRedisClient() *fakeRedisClient {
	return &fakeRedisClient{data: make(map[string]entry)}
}

func (f *fakeRedisClient) Set(_ context.Context, key string, value interface{}, ttl time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	if f.setErr != nil {
		cmd.SetErr(f.setErr)
		return cmd
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		b, _ = json.Marshal(v)
	}
	f.data[key] = entry{value: b, ttl: ttl}
	return cmd
}

func (f *fakeRedisClient) Get(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	if f.getErr != nil {
		cmd.SetErr(f.getErr)
		return cmd
	}
	e, ok := f.data[key]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(string(e.value))
	return cmd
}

func (f *fakeRedisClient) Close() error {
	if f.closeDelay > 0 {
		time.Sleep(f.closeDelay)
	}
	f.closed = true
	return nil
}

func newTestStore(c redisClient) *Store {
	return &Store{client: c}
}

// --- SaveResponse ---

func TestSaveResponse_SetsCorrectKey(t *testing.T) {
	fc := newFakeRedisClient()
	s := newTestStore(fc)

	resp := &internal.Response{Status: "completed", RequestID: "abc-123"}
	if err := s.SaveResponse(context.Background(), resp, time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := fc.data["rpc:abc-123"]; !ok {
		t.Fatalf("expected key 'rpc:abc-123', got keys: %v", keys(fc.data))
	}
}

func TestSaveResponse_SetsTTL(t *testing.T) {
	fc := newFakeRedisClient()
	s := newTestStore(fc)

	resp := &internal.Response{Status: "pending", RequestID: "ttl-test"}
	if err := s.SaveResponse(context.Background(), resp, 5*time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	e := fc.data["rpc:ttl-test"]
	if e.ttl != 5*time.Minute {
		t.Fatalf("expected TTL=5m, got %v", e.ttl)
	}
}

func TestSaveResponse_SetsUpdatedAt(t *testing.T) {
	fc := newFakeRedisClient()
	s := newTestStore(fc)

	before := time.Now().Unix()
	resp := &internal.Response{Status: "completed", RequestID: "ts-test"}
	if err := s.SaveResponse(context.Background(), resp, time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now().Unix()

	if resp.UpdatedAt < before || resp.UpdatedAt > after {
		t.Fatalf("UpdatedAt %d out of range [%d, %d]", resp.UpdatedAt, before, after)
	}
}

func TestSaveResponse_StoreError(t *testing.T) {
	fc := newFakeRedisClient()
	fc.setErr = errors.New("redis write failed")
	s := newTestStore(fc)

	err := s.SaveResponse(context.Background(), &internal.Response{Status: "pending", RequestID: "x"}, time.Minute)
	if err == nil {
		t.Fatal("expected error from Set")
	}
}

// --- GetResponse ---

func TestGetResponse_RoundTrip(t *testing.T) {
	fc := newFakeRedisClient()
	s := newTestStore(fc)

	in := &internal.Response{
		Status:    "completed",
		RequestID: "round-trip",
		Result:    json.RawMessage(`{"block":"0x1"}`),
	}
	if err := s.SaveResponse(context.Background(), in, time.Minute); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, err := s.GetResponse(context.Background(), "round-trip")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Status != "completed" {
		t.Fatalf("expected status=completed, got %s", out.Status)
	}
	if string(out.Result) != `{"block":"0x1"}` {
		t.Fatalf("unexpected result: %s", out.Result)
	}
}

func TestGetResponse_NotFound(t *testing.T) {
	fc := newFakeRedisClient()
	s := newTestStore(fc)

	_, err := s.GetResponse(context.Background(), "missing-id")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestGetResponse_StoreError(t *testing.T) {
	fc := newFakeRedisClient()
	fc.getErr = errors.New("redis read failed")
	s := newTestStore(fc)

	_, err := s.GetResponse(context.Background(), "any")
	if err == nil {
		t.Fatal("expected error from Get")
	}
}

// --- Close ---

func TestStore_Close_Success(t *testing.T) {
	fc := newFakeRedisClient()
	s := newTestStore(fc)

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fc.closed {
		t.Fatal("expected client to be closed")
	}
}

func TestStore_Close_ContextTimeout(t *testing.T) {
	fc := newFakeRedisClient()
	fc.closeDelay = 200 * time.Millisecond
	s := newTestStore(fc)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := s.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

// helper
func keys(m map[string]entry) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
