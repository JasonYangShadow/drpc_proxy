package kafka

import (
	"context"
	"time"

	"github.com/segmentio/kafka-go"
)

type Producer struct {
	writer *kafka.Writer
}

func NewProducer(broker string) (*Producer, error) {
	w := &kafka.Writer{
		Addr:         kafka.TCP(broker),
		Topic:        "rpc_requests",
		Balancer:     &kafka.Hash{},
		Async:        false,
		BatchSize:    100, // batch up to 100 messages
		BatchTimeout: 10 * time.Millisecond,
		Compression:  kafka.Snappy,
		RequiredAcks: kafka.RequireOne,
		MaxAttempts:  3,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return &Producer{writer: w}, nil
}

func (p *Producer) Send(key string, value []byte) error {
	return p.writer.WriteMessages(context.Background(), kafka.Message{
		Key:   []byte(key),
		Value: value,
	})
}

func (p *Producer) SendWithContext(ctx context.Context, key string, value []byte) error {
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(key),
		Value: value,
	})
}
