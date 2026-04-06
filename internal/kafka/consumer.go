package kafka

import (
	"context"
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
	SaveResponses(ctx context.Context, resps []*internal.Response, ttl time.Duration) error
	Close(ctx context.Context) error
}

// readerIface is the subset of *kafka.Reader used by Consumer.
type readerIface interface {
	ReadMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

type Consumer struct {
	readerIface readerIface
	dlqWriter   dlqSender
	store       resultSaver
	workers     int
	batchSize   int
	jobCh       chan kafka.Message
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewConsumer(broker, group, redisAddr string, workers, batchSize int) (*Consumer, error) {
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
		readerIface: r,
		dlqWriter:   w,
		store:       store,
		workers:     workers,
		jobCh:       make(chan kafka.Message, workers*batchSize*internal.KafkaConsumerJobChMultiplier),
		batchSize:   batchSize,
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

func (c *Consumer) Consume(handler func(map[string]kafka.Message) (map[string][]byte, error)) {
	defer close(c.jobCh)

	c.wg.Add(c.workers)
	for i := 0; i < c.workers; i++ {
		go func(id int) {
			defer c.wg.Done()

			batch := make([]kafka.Message, 0, c.batchSize)
			flushTimer := time.NewTimer(internal.KafkaConsumerBatchFlushTimeout)
			defer flushTimer.Stop()

			flush := func() {
				if len(batch) == 0 {
					return
				}

				batchMap := make(map[string]kafka.Message, len(batch))
				for _, m := range batch {
					batchMap[string(m.Key)] = m
				}

				start := time.Now()
				// call the real handler to batch processing messages
				results, err := handler(batchMap)
				metrics.WorkerProcessingDuration.Observe(time.Since(start).Seconds())

				var toCommit []kafka.Message

				if err != nil {
					metrics.WorkerMessagesProcessed.WithLabelValues("failure").Add(float64(len(batch)))
					log.Printf("[worker %d] batch error: %v — sending all %d messages to DLQ", id, err, len(batch))
					if dlqErr := c.sendBatchToDLQ(batch, err); dlqErr != nil {
						log.Printf("[worker %d] CRITICAL: failed to send batch to DLQ: %v", id, dlqErr)
					} else {
						metrics.WorkerDLQSentTotal.Add(float64(len(batch)))
						failResps := make([]*internal.Response, len(batch))
						for i, msg := range batch {
							failResps[i] = &internal.Response{
								RequestID: string(msg.Key),
								Status:    "failed",
								Error:     err.Error(),
							}
							toCommit = append(toCommit, msg)
						}
						ctx, cancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
						if saveErr := c.store.SaveResponses(ctx, failResps, internal.ResultTTL); saveErr != nil {
							log.Printf("[worker %d] failed to save failure statuses to Redis: %v", id, saveErr)
							// as we already have the messages in the DLQ, safe to commit
						}
						cancel()
					}
				} else {
					metrics.WorkerMessagesProcessed.WithLabelValues("success").Add(float64(len(batch)))
					successResps := make([]*internal.Response, 0, len(batch))
					for _, msg := range batch {
						successResps = append(successResps, &internal.Response{
							RequestID: string(msg.Key),
							Status:    "completed",
							Result:    results[string(msg.Key)],
						})
						toCommit = append(toCommit, msg)
					}
					ctx, cancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
					if saveErr := c.store.SaveResponses(ctx, successResps, internal.ResultTTL); saveErr != nil {
						log.Printf("[worker %d] failed to save responses to Redis: %v", id, saveErr)
						toCommit = toCommit[:0] // don't commit if Redis save fails
					}
					cancel()
				}

				if len(toCommit) > 0 {
					ctx, cancel := context.WithTimeout(context.Background(), internal.KafkaConsumerCommitTimeout)
					if err := c.readerIface.CommitMessages(ctx, toCommit...); err != nil {
						log.Printf("[worker %d] commit error: %v", id, err)
					}
					cancel()
				}

				batch = batch[:0]
			}

			for {
				select {
				case msg, ok := <-c.jobCh:
					if !ok {
						flush()
						return
					}
					batch = append(batch, msg)
					if len(batch) >= c.batchSize {
						if !flushTimer.Stop() {
							select {
							case <-flushTimer.C:
							default:
							}
						}
						flush()
						flushTimer.Reset(internal.KafkaConsumerBatchFlushTimeout)
					}
				case <-flushTimer.C:
					flush()
					flushTimer.Reset(internal.KafkaConsumerBatchFlushTimeout)
				}
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
			msg, err := c.readerIface.ReadMessage(ctx)
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

// sendBatchToDLQ sends all messages in a batch to the dead letter queue in one call.
func (c *Consumer) sendBatchToDLQ(msgs []kafka.Message, err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), internal.KafkaDLQWriteTimeout)
	defer cancel()

	if writeErr := c.dlqWriter.WriteMessages(ctx, msgs...); writeErr != nil {
		return fmt.Errorf("failed to write batch to DLQ: %w", writeErr)
	}

	log.Printf("Batch of %d messages sent to DLQ, error=%v", len(msgs), err)
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
		c.readerIface.Close(),
		c.dlqWriter.Close(),
		c.store.Close(ctx),
	)
}
