package kafka

import (
	"context"
	"log"
	"time"

	"drpc_proxy.com/internal"
	"github.com/segmentio/kafka-go"
)

type Consumer struct {
	reader  *kafka.Reader
	workers int
	jobCh   chan kafka.Message
}

func NewConsumer(broker, group string, workers int) (*Consumer, error) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:           []string{broker},
		GroupID:           group,
		Topic:             "rpc_requests",
		MinBytes:          10e3,
		MaxBytes:          10e6,
		MaxWait:           50 * time.Millisecond,
		ReadLagInterval:   -1,
		HeartbeatInterval: 3 * time.Second,
		SessionTimeout:    30 * time.Second,
		RebalanceTimeout:  30 * time.Second,
		CommitInterval:    -1, // manual confirmation
	})

	return &Consumer{
		reader:  r,
		workers: workers,
		jobCh:   make(chan kafka.Message, workers*2),
	}, nil
}

func (c *Consumer) Consume(handler func([]byte) error) {
	for i := 0; i < c.workers; i++ {
		go func(id int) {
			for msg := range c.jobCh {
				if err := handler(msg.Value); err != nil {
					log.Printf("[worker %d] handler error: %v", id, err)
					continue
				}

				// manual confirmation
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := c.reader.CommitMessages(ctx, msg); err != nil {
					log.Printf("[worker %d] commit error: %v", id, err)
				}
				cancel()
			}
		}(i)
	}

	bgCtx := context.Background()
	for {
		deadline := time.Now().Add(internal.KafkaTimeout)
		ctx, cancel := context.WithDeadline(bgCtx, deadline)
		msg, err := c.reader.ReadMessage(ctx)
		cancel()

		if err != nil {
			log.Println("Kafka read error:", err)
			continue
		}

		c.jobCh <- msg
	}
}
