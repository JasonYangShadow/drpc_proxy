package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

func (f *fakeResultSaver) SaveResponses(_ context.Context, resps []*internal.Response, _ time.Duration) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	for _, resp := range resps {
		cp := *resp
		f.saved = append(f.saved, &cp)
	}
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

// buildConsumer builds a minimal Consumer for unit testing sendToDLQ / sendBatchToDLQ.
func buildConsumer(store resultSaver, dlq dlqSender) *Consumer {
	return &Consumer{
		store:     store,
		dlqWriter: dlq,
	}
}

func kafkaMsg(key, value string) segkafka.Message {
	kmJSON, _ := json.Marshal(internal.KafkaMessage{
		RequestID: key,
		Raw:       json.RawMessage(value),
	})
	return segkafka.Message{
		Key:   []byte(key),
		Value: kmJSON,
	}
}

// --- sendBatchToDLQ tests ---

func TestSendBatchToDLQ_Success(t *testing.T) {
	dlq := &fakeDLQSender{}
	c := buildConsumer(&fakeResultSaver{}, dlq)

	msgs := []segkafka.Message{
		kafkaMsg("req-010", `{"method":"eth_call"}`),
		kafkaMsg("req-011", `{"method":"eth_blockNumber"}`),
	}
	if err := c.sendBatchToDLQ(msgs, errors.New("upstream failure")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dlq.written) != 2 {
		t.Fatalf("expected 2 messages written to DLQ, got %d", len(dlq.written))
	}
	if string(dlq.written[0].Key) != "req-010" {
		t.Fatalf("expected key=req-010, got %s", dlq.written[0].Key)
	}
	if string(dlq.written[1].Key) != "req-011" {
		t.Fatalf("expected key=req-011, got %s", dlq.written[1].Key)
	}
}

func TestSendBatchToDLQ_WriteError(t *testing.T) {
	dlq := &fakeDLQSender{writeErr: errors.New("kafka unavailable")}
	c := buildConsumer(&fakeResultSaver{}, dlq)

	msgs := []segkafka.Message{kafkaMsg("req-012", `{}`)}
	if err := c.sendBatchToDLQ(msgs, errors.New("upstream failure")); err == nil {
		t.Fatal("expected error when DLQ write fails")
	}
}

func TestSendBatchToDLQ_PreservesMessages(t *testing.T) {
	dlq := &fakeDLQSender{}
	c := buildConsumer(&fakeResultSaver{}, dlq)

	originals := []segkafka.Message{
		{
			Key:       []byte("req-013"),
			Value:     []byte(`{"id":1}`),
			Topic:     internal.KafkaTopic,
			Partition: 3,
			Offset:    42,
		},
		{
			Key:       []byte("req-014"),
			Value:     []byte(`{"id":2}`),
			Topic:     internal.KafkaTopic,
			Partition: 3,
			Offset:    43,
		},
	}
	if err := c.sendBatchToDLQ(originals, errors.New("err")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dlq.written) != 2 {
		t.Fatalf("expected 2 messages in DLQ, got %d", len(dlq.written))
	}
	for i, sent := range dlq.written {
		if string(sent.Key) != string(originals[i].Key) {
			t.Fatalf("[%d] key mismatch: got %s", i, sent.Key)
		}
		if string(sent.Value) != string(originals[i].Value) {
			t.Fatalf("[%d] value mismatch: got %s", i, sent.Value)
		}
	}
}

// --- Consume (batch) tests ---

// fakeReader feeds a fixed list of messages then blocks, simulating kafka.Reader.
type fakeReader struct {
	mu   sync.Mutex
	msgs []segkafka.Message
	pos  int

	committed []segkafka.Message
	closed    bool
}

func (f *fakeReader) ReadMessage(ctx context.Context) (segkafka.Message, error) {
	f.mu.Lock()
	if f.pos < len(f.msgs) {
		msg := f.msgs[f.pos]
		f.pos++
		f.mu.Unlock()
		return msg, nil
	}
	f.mu.Unlock()
	// No more messages: block until context is cancelled.
	<-ctx.Done()
	return segkafka.Message{}, ctx.Err()
}

func (f *fakeReader) CommitMessages(_ context.Context, msgs ...segkafka.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, msgs...)
	return nil
}

func (f *fakeReader) Close() error {
	f.closed = true
	return nil
}

// buildTestConsumer wires a Consumer with fakes, bypassing NewConsumer.
func buildTestConsumer(t *testing.T, r readerIface, store resultSaver, dlq dlqSender) *Consumer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	return &Consumer{
		readerIface: r,
		dlqWriter:   dlq,
		store:       store,
		workers:     1,
		batchSize:   internal.DefaultKafkaConsumerBatchSize,
		jobCh:       make(chan segkafka.Message, internal.DefaultKafkaConsumerBatchSize*2),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// runConsume drives the consumer: feeds msgs, waits for all to be committed or
// DLQ'd, then cancels.
func runConsume(t *testing.T, c *Consumer, handler func(map[string]segkafka.Message) (map[string][]byte, error), wantProcessed int) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		c.Consume(handler)
		close(done)
	}()

	// Poll until wantProcessed messages appear in committed+DLQ or timeout.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for messages to be processed")
		case <-time.After(5 * time.Millisecond):
			r := c.readerIface.(*fakeReader)
			dlq := c.dlqWriter.(*fakeDLQSender)
			r.mu.Lock()
			committed := len(r.committed)
			r.mu.Unlock()
			if committed+len(dlq.written) >= wantProcessed {
				c.cancel()
				<-done
				return
			}
		}
	}
}

// TestConsume_AllSuccess: all messages processed successfully → committed, saved as completed.
func TestConsume_AllSuccess(t *testing.T) {
	msgs := []segkafka.Message{
		kafkaMsg("r1", `{"v":1}`),
		kafkaMsg("r2", `{"v":2}`),
		kafkaMsg("r3", `{"v":3}`),
	}
	fr := &fakeReader{msgs: msgs}
	store := &fakeResultSaver{}
	dlq := &fakeDLQSender{}

	c := buildTestConsumer(t, fr, store, dlq)

	handler := func(msgs map[string]segkafka.Message) (map[string][]byte, error) {
		result := make(map[string][]byte, len(msgs))
		for requestID := range msgs {
			result[requestID] = []byte(`{"result":"ok"}`)
		}
		return result, nil
	}

	runConsume(t, c, handler, len(msgs))

	if len(dlq.written) != 0 {
		t.Fatalf("expected 0 DLQ messages, got %d", len(dlq.written))
	}
	fr.mu.Lock()
	committed := len(fr.committed)
	fr.mu.Unlock()
	if committed != len(msgs) {
		t.Fatalf("expected %d committed, got %d", len(msgs), committed)
	}
	for _, r := range store.saved {
		if r.Status != "completed" {
			t.Fatalf("expected status=completed, got %s", r.Status)
		}
	}
}

// TestConsume_AnyFailureFailsAll: one handler error → all messages in that
// batch go to DLQ with failed status.
func TestConsume_AnyFailureFailsAll(t *testing.T) {
	msgs := []segkafka.Message{
		kafkaMsg("r1", `{"v":1}`),
		kafkaMsg("r2", `{"v":2}`),
		kafkaMsg("r3", `{"v":3}`),
	}
	fr := &fakeReader{msgs: msgs}
	store := &fakeResultSaver{}
	dlq := &fakeDLQSender{}

	c := buildTestConsumer(t, fr, store, dlq)

	batchErr := errors.New("upstream 500")
	handler := func(msgs map[string]segkafka.Message) (map[string][]byte, error) {
		return nil, batchErr
	}

	runConsume(t, c, handler, len(msgs))

	if len(dlq.written) != len(msgs) {
		t.Fatalf("expected all %d messages in DLQ, got %d", len(msgs), len(dlq.written))
	}
	for _, r := range store.saved {
		if r.Status != "failed" {
			t.Fatalf("expected status=failed, got %s", r.Status)
		}
		if r.Error == "" {
			t.Fatal("expected Error field to be set")
		}
	}
}

// TestConsume_DLQWriteFailure: if DLQ write fails for a message, that message
// must NOT be committed (so it gets redelivered).
func TestConsume_DLQWriteFailure(t *testing.T) {
	msgs := []segkafka.Message{kafkaMsg("r1", `{"v":1}`)}
	fr := &fakeReader{msgs: msgs}
	store := &fakeResultSaver{}
	dlq := &fakeDLQSender{writeErr: errors.New("kafka unavailable")}

	c := buildTestConsumer(t, fr, store, dlq)

	handler := func(msgs map[string]segkafka.Message) (map[string][]byte, error) {
		return nil, errors.New("upstream error")
	}

	// Give it time to attempt processing; check nothing was committed.
	done := make(chan struct{})
	go func() {
		c.Consume(handler)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	c.cancel()
	<-done

	fr.mu.Lock()
	committed := len(fr.committed)
	fr.mu.Unlock()
	if committed != 0 {
		t.Fatalf("expected 0 committed when DLQ write fails, got %d", committed)
	}
}

// TestConsume_TimerFlush: fewer messages than batch size are flushed by the
// timer rather than waiting forever.
func TestConsume_TimerFlush(t *testing.T) {
	// Send 2 messages — well below DefaultKafkaConsumerBatchSize.
	msgs := []segkafka.Message{
		kafkaMsg("r1", `{"v":1}`),
		kafkaMsg("r2", `{"v":2}`),
	}
	fr := &fakeReader{msgs: msgs}
	store := &fakeResultSaver{}
	dlq := &fakeDLQSender{}

	c := buildTestConsumer(t, fr, store, dlq)

	handler := func(msgs map[string]segkafka.Message) (map[string][]byte, error) {
		result := make(map[string][]byte, len(msgs))
		for requestID := range msgs {
			result[requestID] = []byte(`{"result":"ok"}`)
		}
		return result, nil
	}

	// Should flush via timer without needing a full batch.
	runConsume(t, c, handler, len(msgs))

	fr.mu.Lock()
	committed := len(fr.committed)
	fr.mu.Unlock()
	if committed != len(msgs) {
		t.Fatalf("expected %d committed via timer flush, got %d", len(msgs), committed)
	}
}
