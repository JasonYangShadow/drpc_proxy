package kafka

import (
	"context"

	"drpc_proxy.com/internal"
	"github.com/segmentio/kafka-go"
)

type Producer struct {
	writer *kafka.Writer
	ctx    context.Context
	cancel context.CancelFunc
}

func NewProducer(broker string) (*Producer, error) {
	w := &kafka.Writer{
		Addr:         kafka.TCP(broker),
		Topic:        internal.KafkaTopic,
		Balancer:     &kafka.Hash{},
		Async:        false,
		BatchSize:    internal.KafkaProducerBatchSize,
		BatchTimeout: internal.KafkaProducerBatchTimeout,
		Compression:  kafka.Snappy,
		RequiredAcks: kafka.RequireOne,
		MaxAttempts:  internal.KafkaProducerMaxAttempts,
		ReadTimeout:  internal.KafkaProducerReadTimeout,
		WriteTimeout: internal.KafkaProducerWriteTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Producer{
		writer: w,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

func (p *Producer) Send(key string, value []byte) error {
	return p.writer.WriteMessages(p.ctx, kafka.Message{
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

func (p *Producer) Close(ctx context.Context) error {
	p.cancel() // Cancel parent context

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- p.writer.Close()
	}()

	select {
	case err := <-doneCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
