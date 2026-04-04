package kafka

import (
	"context"
	"log"
	"sync"

	"drpc_proxy.com/internal"
	"github.com/segmentio/kafka-go"
)

type Consumer struct {
	reader  *kafka.Reader
	workers int
	jobCh   chan kafka.Message
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewConsumer(broker, group string, workers int) (*Consumer, error) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:           []string{broker},
		GroupID:           group,
		Topic:             internal.KafkaTopic,
		MinBytes:          internal.KafkaConsumerMinBytes,
		MaxBytes:          internal.KafkaConsumerMaxBytes,
		MaxWait:           internal.KafkaConsumerMaxWait,
		ReadLagInterval:   internal.KafkaConsumerReadLagInterval,
		HeartbeatInterval: internal.KafkaConsumerHeartbeatInterval,
		SessionTimeout:    internal.KafkaConsumerSessionTimeout,
		RebalanceTimeout:  internal.KafkaConsumerRebalanceTimeout,
		CommitInterval:    internal.KafkaConsumerCommitInterval, // manual confirmation
	})

	ctx, cancel := context.WithCancel(context.Background())

	return &Consumer{
		reader:  r,
		workers: workers,
		jobCh:   make(chan kafka.Message, workers*internal.KafkaConsumerJobChMultiplier),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

func (c *Consumer) Consume(handler func([]byte) error) {
	defer close(c.jobCh)

	c.wg.Add(c.workers)
	for i := 0; i < c.workers; i++ {
		go func(id int) {
			defer c.wg.Done()
			for msg := range c.jobCh {
				if err := handler(msg.Value); err != nil {
					log.Printf("[worker %d] handler error: %v", id, err)
					continue
				}

				// manual confirmation
				ctx, cancel := context.WithTimeout(c.ctx, internal.KafkaConsumerCommitTimeout)
				if err := c.reader.CommitMessages(ctx, msg); err != nil {
					log.Printf("[worker %d] commit error: %v", id, err)
				}
				cancel()
			}
		}(i)
	}

	for {
		select {
		case <-c.ctx.Done():
			log.Println("Consumer context cancelled...")
			return
		default:
			ctx, cancel := context.WithTimeout(c.ctx, internal.KafkaTimeout)
			msg, err := c.reader.ReadMessage(ctx)
			cancel()

			if err != nil {
				// Only log non-timeout errors
				if err != context.DeadlineExceeded {
					log.Println("Kafka read error:", err)
				}
				continue
			}

			select {
			case c.jobCh <- msg:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

func (c *Consumer) Close(ctx context.Context) error {
	c.cancel() // Cancel parent context

	doneCh := make(chan error, 1)
	go func() {
		c.wg.Wait()
		doneCh <- c.reader.Close()
	}()

	select {
	case err := <-doneCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
