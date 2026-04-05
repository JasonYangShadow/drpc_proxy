package proxy

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/metrics"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// responseStorer is the subset of redis.Store used by Handler.
type responseStorer interface {
	SaveResponse(ctx context.Context, resp *internal.Response, ttl time.Duration) error
	GetResponse(ctx context.Context, requestID string) (*internal.Response, error)
}

// messageSender is the subset of kafka.Producer used by Handler.
type messageSender interface {
	SendWithContext(ctx context.Context, key string, value []byte) error
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type Handler struct {
	producer  messageSender
	store     responseStorer
	semaphore chan struct{}

	kafkaCh   chan *internal.KafkaMessage
	onceStart sync.Once
	wg        sync.WaitGroup
}

func NewHandler(p messageSender, s responseStorer, maxConcurrent int, kafkaWorkers int) *Handler {
	if maxConcurrent <= 0 {
		maxConcurrent = internal.DefaultMaxConcurrent
	}
	if kafkaWorkers <= 0 {
		kafkaWorkers = internal.DefaultKafkaWorkers
	}

	h := &Handler{
		producer:  p,
		store:     s,
		semaphore: make(chan struct{}, maxConcurrent),
		kafkaCh:   make(chan *internal.KafkaMessage, maxConcurrent*internal.KafkaChannelMultiplier),
	}

	h.startKafkaWorkers(kafkaWorkers)
	return h
}

func (h *Handler) startKafkaWorkers(n int) {
	h.onceStart.Do(func() {
		for i := 0; i < n; i++ {
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				h.kafkaWorker()
			}()
		}
	})
}

func (h *Handler) kafkaWorker() {
	buf := make([]byte, internal.KafkaMessageMaxSize)

	for msg := range h.kafkaCh {
		b, err := sonic.Marshal(msg)
		if err != nil {
			redisCtx, cancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "failed",
				RequestID: msg.RequestID,
				Error:     "marshal error",
			}, internal.StatusTTL)
			cancel()
			continue
		}

		if len(b) > len(buf) {
			redisCtx, cancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "failed",
				RequestID: msg.RequestID,
				Error:     "payload too large",
			}, internal.StatusTTL)
			cancel()
			continue
		}

		n := copy(buf, b)

		kafkaCtx, kcancel := context.WithTimeout(context.Background(), internal.KafkaTimeout)
		err = h.producer.SendWithContext(kafkaCtx, msg.RequestID, buf[:n])
		kcancel()

		redisCtx, rcancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
		if err != nil {
			metrics.ProxyKafkaErrorsTotal.Inc()
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "failed",
				RequestID: msg.RequestID,
				Error:     err.Error(),
			}, internal.StatusTTL)
		} else {
			metrics.ProxyKafkaSentTotal.Inc()
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "queued",
				RequestID: msg.RequestID,
			}, internal.StatusTTL)
		}
		rcancel()
	}
}

func (h *Handler) HandleRPC(w http.ResponseWriter, r *http.Request) {
	metrics.ProxyRequestsTotal.Inc()
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "server overloaded, please retry later"})
		return
	}

	// only read limited size
	limitedBody := io.LimitReader(r.Body, internal.MaxUpstreamRequestSize+1)
	raw, err := io.ReadAll(limitedBody)
	if err != nil || len(raw) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "invalid request body"})
		return
	}

	// check if the request is beyond the maximum request size (malicious)
	if len(raw) > internal.MaxUpstreamRequestSize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "request too large"})
		return
	}

	// message unique id for each request
	reqID := uuid.New().String()

	// Set initial pending status in Redis
	redisCtx, redisCancel := context.WithTimeout(r.Context(), internal.RedisTimeout)
	defer redisCancel()
	if err := h.store.SaveResponse(redisCtx, &internal.Response{
		Status:    "pending",
		RequestID: reqID,
	}, internal.StatusTTL); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "failed to initialize request"})
		return
	}

	msg := &internal.KafkaMessage{
		RequestID:  reqID,
		Raw:        raw,
		ReceivedAt: time.Now().Unix(),
	}

	select {
	case h.kafkaCh <- msg:
	default:
		// Clean up the pending Redis key since the request is being rejected
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), internal.RedisTimeout)
		_ = h.store.SaveResponse(cleanCtx, &internal.Response{
			Status:    "failed",
			RequestID: reqID,
			Error:     "kafka queue full",
		}, internal.StatusTTL)
		cleanCancel()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "kafka queue full, please retry later"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(&internal.Response{
		Status:    "accepted",
		RequestID: reqID,
	})
}

func (h *Handler) HandleResult(w http.ResponseWriter, r *http.Request) {
	requestID := r.URL.Query().Get("request_id")
	if requestID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "request_id parameter is required"})
		return
	}

	redisCtx, redisCancel := context.WithTimeout(r.Context(), internal.RedisTimeout)
	defer redisCancel()

	// Get unified response
	resp, err := h.store.GetResponse(redisCtx, requestID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "request not found"})
		return
	}

	// Return appropriate status code based on response status
	w.Header().Set("Content-Type", "application/json")
	switch resp.Status {
	case "pending", "queued":
		w.WriteHeader(http.StatusAccepted)
	case "failed":
		w.WriteHeader(http.StatusInternalServerError)
	case "completed":
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(resp)
}

func (h *Handler) Close() {
	close(h.kafkaCh) // signals kafkaWorker goroutines to exit
	h.wg.Wait()      // wait for all workers to finish in-flight writes
}
