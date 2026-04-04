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
	jobCh   chan []byte
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
		CommitInterval:    0,
	})

	return &Consumer{
		reader:  r,
		workers: workers,
		jobCh:   make(chan []byte, workers*2),
	}, nil
}

func (c *Consumer) Consume(handler func([]byte) error) {
	for i := 0; i < c.workers; i++ {
		go func(id int) {
			for msg := range c.jobCh {
				if err := handler(msg); err != nil {
					log.Printf("[worker %d] handler error: %v", id, err)
				}
			}
		}(i)
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), internal.KafkaTimeout)
		msg, err := c.reader.ReadMessage(ctx)
		cancel()

		if err != nil {
			log.Println("Kafka read error:", err)
			continue
		}

		// 投递到 worker pool
		c.jobCh <- msg.Value
	}
}
