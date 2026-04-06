package kafka

import (
"context"
"errors"
"testing"
"time"

segkafka "github.com/segmentio/kafka-go"
)

// fakeKafkaWriter implements kafkaWriter for testing.
type fakeKafkaWriter struct {
	written  []segkafka.Message
	writeErr error
	// closeDelay simulates a slow Close() to test context cancellation.
	closeDelay time.Duration
	closed     bool
}

func (f *fakeKafkaWriter) WriteMessages(_ context.Context, msgs ...segkafka.Message) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, msgs...)
	return nil
}

func (f *fakeKafkaWriter) Close() error {
	if f.closeDelay > 0 {
		time.Sleep(f.closeDelay)
	}
	f.closed = true
	return nil
}

func newTestProducer(w kafkaWriter) *Producer {
	return &Producer{writer: w}
}

// --- SendWithContext ---

func TestProducer_Send_Success(t *testing.T) {
	fw := &fakeKafkaWriter{}
	p := newTestProducer(fw)

	ctx := context.Background()
	if err := p.SendWithContext(ctx, "req-001", []byte(`{"method":"eth_call"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fw.written) != 1 {
		t.Fatalf("expected 1 message written, got %d", len(fw.written))
	}
	msg := fw.written[0]
	if string(msg.Key) != "req-001" {
		t.Fatalf("expected key=req-001, got %s", msg.Key)
	}
	if string(msg.Value) != `{"method":"eth_call"}` {
		t.Fatalf("unexpected value: %s", msg.Value)
	}
}

func TestProducer_Send_WriterError(t *testing.T) {
	fw := &fakeKafkaWriter{writeErr: errors.New("broker unavailable")}
	p := newTestProducer(fw)

	err := p.SendWithContext(context.Background(), "req-002", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error from writer")
	}
}

func TestProducer_Send_ContextCancelled(t *testing.T) {
	fw := &fakeKafkaWriter{}
	p := newTestProducer(fw)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// The fake ignores the context, but the real writer honours it.
	// Here we only confirm the call is delegated (no panic / incorrect routing).
	_ = p.SendWithContext(ctx, "req-003", []byte(`{}`))
}

// --- Close ---

func TestProducer_Close_Success(t *testing.T) {
	fw := &fakeKafkaWriter{}
	p := newTestProducer(fw)

	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fw.closed {
		t.Fatal("expected writer to be closed")
	}
}

func TestProducer_Close_ContextTimeout(t *testing.T) {
	// Simulate a writer that takes 200ms to close.
	fw := &fakeKafkaWriter{closeDelay: 200 * time.Millisecond}
	p := newTestProducer(fw)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := p.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestProducer_Send_EmptyKeyAndValue(t *testing.T) {
	fw := &fakeKafkaWriter{}
	p := newTestProducer(fw)

	if err := p.SendWithContext(context.Background(), "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fw.written) != 1 {
		t.Fatalf("expected 1 message, got %d", len(fw.written))
	}
	if len(fw.written[0].Key) != 0 {
		t.Fatalf("expected empty key, got %s", fw.written[0].Key)
	}
}
