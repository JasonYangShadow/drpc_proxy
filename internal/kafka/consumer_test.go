package kafka

import (
"context"
"encoding/json"
"errors"
"testing"
"time"

"drpc_proxy.com/internal"
segkafka "github.com/segmentio/kafka-go"
)

// --- fakes ---

type fakeResultSaver struct {
	saved   []*internal.Response
	saveErr error
	closed  bool
}

func (f *fakeResultSaver) SaveResponse(_ context.Context, resp *internal.Response, _ time.Duration) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	cp := *resp
	f.saved = append(f.saved, &cp)
	return nil
}

func (f *fakeResultSaver) Close(_ context.Context) error {
	f.closed = true
	return nil
}

type fakeDLQSender struct {
	written  []segkafka.Message
	writeErr error
	closed   bool
}

func (f *fakeDLQSender) WriteMessages(_ context.Context, msgs ...segkafka.Message) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, msgs...)
	return nil
}

func (f *fakeDLQSender) Close() error {
	f.closed = true
	return nil
}

// buildConsumer builds a minimal Consumer for unit testing saveResponse / sendToDLQ.
func buildConsumer(store resultSaver, dlq dlqSender) *Consumer {
	return &Consumer{
		store:     store,
		dlqWriter: dlq,
	}
}

func kafkaMsg(key, value string) segkafka.Message {
	return segkafka.Message{
		Key:   []byte(key),
		Value: []byte(value),
	}
}

// --- saveResponse tests ---

func TestSaveResponse_Success(t *testing.T) {
	store := &fakeResultSaver{}
	c := buildConsumer(store, &fakeDLQSender{})

	respBody := json.RawMessage(`{"block":"0x1"}`)
	msg := kafkaMsg("req-001", `{}`)

	if err := c.saveResponse(msg, respBody, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 saved entry, got %d", len(store.saved))
	}
	r := store.saved[0]
	if r.Status != "completed" {
		t.Fatalf("expected status=completed, got %s", r.Status)
	}
	if r.RequestID != "req-001" {
		t.Fatalf("expected RequestID=req-001, got %s", r.RequestID)
	}
	if string(r.Result) != `{"block":"0x1"}` {
		t.Fatalf("unexpected result: %s", r.Result)
	}
	if r.Error != "" {
		t.Fatalf("unexpected error field: %s", r.Error)
	}
}

func TestSaveResponse_Failure(t *testing.T) {
	store := &fakeResultSaver{}
	c := buildConsumer(store, &fakeDLQSender{})

	msg := kafkaMsg("req-002", `{}`)
	upErr := errors.New("upstream 500")

	if err := c.saveResponse(msg, nil, upErr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 saved entry, got %d", len(store.saved))
	}
	r := store.saved[0]
	if r.Status != "failed" {
		t.Fatalf("expected status=failed, got %s", r.Status)
	}
	if r.Error == "" {
		t.Fatal("expected Error field to be populated")
	}
	if r.Result != nil {
		t.Fatalf("expected Result to be nil on failure, got %s", r.Result)
	}
}

func TestSaveResponse_RedisError(t *testing.T) {
	store := &fakeResultSaver{saveErr: errors.New("redis down")}
	c := buildConsumer(store, &fakeDLQSender{})

	msg := kafkaMsg("req-003", `{}`)
	err := c.saveResponse(msg, []byte(`{}`), nil)
	if err == nil {
		t.Fatal("expected error when Redis save fails")
	}
}

// --- sendToDLQ tests ---

func TestSendToDLQ_Success(t *testing.T) {
	dlq := &fakeDLQSender{}
	c := buildConsumer(&fakeResultSaver{}, dlq)

	msg := kafkaMsg("req-010", `{"method":"eth_call"}`)
	if err := c.sendToDLQ(msg, errors.New("upstream failure")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dlq.written) != 1 {
		t.Fatalf("expected 1 message written to DLQ, got %d", len(dlq.written))
	}
	sent := dlq.written[0]
	if string(sent.Key) != "req-010" {
		t.Fatalf("expected key=req-010, got %s", sent.Key)
	}
	if string(sent.Value) != `{"method":"eth_call"}` {
		t.Fatalf("unexpected value: %s", sent.Value)
	}
}

func TestSendToDLQ_WriteError(t *testing.T) {
	dlq := &fakeDLQSender{writeErr: errors.New("kafka unavailable")}
	c := buildConsumer(&fakeResultSaver{}, dlq)

	msg := kafkaMsg("req-011", `{}`)
	err := c.sendToDLQ(msg, errors.New("upstream failure"))
	if err == nil {
		t.Fatal("expected error when DLQ write fails")
	}
}

func TestSendToDLQ_PreservesOriginalMessage(t *testing.T) {
	dlq := &fakeDLQSender{}
	c := buildConsumer(&fakeResultSaver{}, dlq)

	original := segkafka.Message{
		Key:       []byte("req-012"),
		Value:     []byte(`{"id":1}`),
		Topic:     internal.KafkaTopic,
		Partition: 3,
		Offset:    42,
	}
	if err := c.sendToDLQ(original, errors.New("err")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sent := dlq.written[0]
	// Key and value must be preserved; topic/partition/offset forwarded as-is.
	if string(sent.Key) != "req-012" {
		t.Fatalf("key mismatch: %s", sent.Key)
	}
	if string(sent.Value) != `{"id":1}` {
		t.Fatalf("value mismatch: %s", sent.Value)
	}
}
