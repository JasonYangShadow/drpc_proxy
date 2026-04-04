package kafka

import (
	"context"
	"log"

	"github.com/segmentio/kafka-go"
)

type Consumer struct {
	reader *kafka.Reader
}

func NewConsumer(broker, group string) (*Consumer, error) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{broker},
		GroupID: group,
		Topic:   "rpc_requests",
	})
	return &Consumer{reader: r}, nil
}

func (c *Consumer) Consume(topic string, handler func([]byte) error) {
	for {
		msg, err := c.reader.ReadMessage(context.Background())
		if err != nil {
			log.Println("Kafka read error:", err)
			continue
		}

		if err := handler(msg.Value); err != nil {
			log.Println("Handler error:", err)
		}
	}
}
