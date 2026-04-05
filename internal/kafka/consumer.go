package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/metrics"
	"drpc_proxy.com/internal/redis"
	"github.com/segmentio/kafka-go"
)

// dlqSender is the subset of *kafka.Writer used by Consumer.
type dlqSender interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// resultSaver is the subset of *redis.Store used by Consumer.
type resultSaver interface {
	SaveResponse(ctx context.Context, resp *internal.Response, ttl time.Duration) error
	Close(ctx context.Context) error
}

type Consumer struct {
	reader    *kafka.Reader
	dlqWriter dlqSender
	store     resultSaver
	workers   int
	jobCh     chan kafka.Message
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewConsumer(broker, group, redisAddr string, workers int) (*Consumer, error) {
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

	w := &kafka.Writer{
		Addr:         kafka.TCP(broker),
		Topic:        internal.KafkaDLQTopic,
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

	// Create Redis store
	store := redis.NewStore(redisAddr)

	return &Consumer{
		reader:    r,
		dlqWriter: w,
		store:     store,
		workers:   workers,
		jobCh:     make(chan kafka.Message, workers*internal.KafkaConsumerJobChMultiplier),
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

func (c *Consumer) Consume(handler func([]byte) ([]byte, error)) {
	defer close(c.jobCh)

	c.wg.Add(c.workers)
	for i := 0; i < c.workers; i++ {
		go func(id int) {
			defer c.wg.Done()
			for msg := range c.jobCh {
				start := time.Now()
				resp, err := handler(msg.Value)
				metrics.WorkerProcessingDuration.Observe(time.Since(start).Seconds())

				if err != nil {
					metrics.WorkerMessagesProcessed.WithLabelValues("failure").Inc()
					log.Printf("[worker %d] handler error: %v, sending to DLQ", id, err)

					// Send to DLQ
					if dlqErr := c.sendToDLQ(msg, err); dlqErr != nil {
						log.Printf("[worker %d] CRITICAL: failed to send message to DLQ: %v", id, dlqErr)
						// Don't commit the message if DLQ also fails
						continue
					}
					metrics.WorkerDLQSentTotal.Inc()

					// Save failure status to Redis
					if saveErr := c.saveResponse(msg, nil, err); saveErr != nil {
						log.Printf("[worker %d] failed to save failure status to Redis: %v", id, saveErr)
					}
				} else {
					metrics.WorkerMessagesProcessed.WithLabelValues("success").Inc()
					// Handler succeeded, save success response to Redis
					if saveErr := c.saveResponse(msg, resp, nil); saveErr != nil {
						log.Printf("[worker %d] failed to save response to Redis: %v", id, saveErr)
						// Don't commit if Redis save fails
						continue
					}
				}

				// manual confirmation (commit on success or after DLQ send)
				ctx, cancel := context.WithTimeout(context.Background(), internal.KafkaConsumerCommitTimeout)
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
				// Only log non-timeout errors; kafka-go wraps context errors so use errors.Is
				if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
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

// saveResponse saves the response (success or failure) to Redis
func (c *Consumer) saveResponse(msg kafka.Message, respBody []byte, err error) error {
	// Create response object
	resp := &internal.Response{
		RequestID: string(msg.Key),
		Result:    nil,
	}

	if err != nil {
		// Failure case
		resp.Status = "failed"
		resp.Error = err.Error()
	} else {
		// Success case
		resp.Status = "completed"
		resp.Result = json.RawMessage(respBody)
	}

	// Save to Redis with TTL
	ctx, cancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
	defer cancel()

	if saveErr := c.store.SaveResponse(ctx, resp, internal.ResultTTL); saveErr != nil {
		return fmt.Errorf("failed to save to Redis: %w", saveErr)
	}

	return nil
}

// sendToDLQ sends a failed message to the dead letter queue
func (c *Consumer) sendToDLQ(msg kafka.Message, err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), internal.KafkaDLQWriteTimeout)
	defer cancel()

	if writeErr := c.dlqWriter.WriteMessages(ctx, msg); writeErr != nil {
		return fmt.Errorf("failed to write to DLQ: %w", writeErr)
	}

	log.Printf("Message sent to DLQ: topic=%s partition=%d offset=%d error=%v",
		msg.Topic, msg.Partition, msg.Offset, err)

	return nil
}

func (c *Consumer) Close(ctx context.Context) error {
	c.cancel() // stop the read loop and jobCh feeding

	// Wait for in-flight workers to finish, or until shutdown timeout
	waitDone := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	// All workers done, close resources
	return errors.Join(
		c.reader.Close(),
		c.dlqWriter.Close(),
		c.store.Close(ctx),
	)
}
