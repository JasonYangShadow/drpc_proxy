package proxy

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/redis"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

type ErrorResponse struct {
	Error string `json:"error"`
}

type Handler struct {
	producer  *kafka.Producer
	store     *redis.Store
	semaphore chan struct{}

	kafkaCh   chan *internal.KafkaMessage
	onceStart sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewHandler(p *kafka.Producer, s *redis.Store, maxConcurrent int, kafkaWorkers int) *Handler {
	if maxConcurrent <= 0 {
		maxConcurrent = internal.DefaultMaxConcurrent
	}
	if kafkaWorkers <= 0 {
		kafkaWorkers = internal.DefaultKafkaWorkers
	}

	ctx, cancel := context.WithCancel(context.Background())

	h := &Handler{
		producer:  p,
		store:     s,
		semaphore: make(chan struct{}, maxConcurrent),
		kafkaCh:   make(chan *internal.KafkaMessage, maxConcurrent*internal.KafkaChannelMultiplier),
		ctx:       ctx,
		cancel:    cancel,
	}

	h.startKafkaWorkers(kafkaWorkers)
	return h
}

func (h *Handler) startKafkaWorkers(n int) {
	h.onceStart.Do(func() {
		for i := 0; i < n; i++ {
			go h.kafkaWorker()
		}
	})
}

func (h *Handler) kafkaWorker() {
	buf := make([]byte, internal.KafkaMessageMaxSize)

	for msg := range h.kafkaCh {
		b, err := sonic.Marshal(msg)
		if err != nil {
			redisCtx, cancel := context.WithTimeout(h.ctx, internal.RedisTimeout)
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "failed",
				RequestID: msg.RequestID,
				Error:     "marshal error",
			}, internal.StatusTTL)
			cancel()
			continue
		}

		if len(b) > len(buf) {
			redisCtx, cancel := context.WithTimeout(h.ctx, internal.RedisTimeout)
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "failed",
				RequestID: msg.RequestID,
				Error:     "payload too large",
			}, internal.StatusTTL)
			cancel()
			continue
		}

		n := copy(buf, b)

		kafkaCtx, kcancel := context.WithTimeout(h.ctx, internal.KafkaTimeout)
		err = h.producer.SendWithContext(kafkaCtx, msg.RequestID, buf[:n])
		kcancel()

		redisCtx, rcancel := context.WithTimeout(h.ctx, internal.RedisTimeout)
		if err != nil {
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "failed",
				RequestID: msg.RequestID,
				Error:     err.Error(),
			}, internal.StatusTTL)
		} else {
			_ = h.store.SaveResponse(redisCtx, &internal.Response{
				Status:    "queued",
				RequestID: msg.RequestID,
			}, internal.StatusTTL)
		}
		rcancel()
	}
}

func (h *Handler) HandleRPC(w http.ResponseWriter, r *http.Request) {
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "server overloaded, please retry later"})
		return
	}

	// only read limited size
	limitedBody := io.LimitReader(r.Body, internal.MaxUpstreamRequestSize+1)
	raw, err := io.ReadAll(limitedBody)
	if err != nil || len(raw) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "invalid request body"})
		return
	}

	// check if the request is beyond the maximum request size (malicious)
	if len(raw) > internal.MaxUpstreamRequestSize {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "request too large"})
		return
	}

	// message unique id for each request
	reqID := uuid.New().String()

	// Set initial pending status in Redis
	redisCtx, redisCancel := context.WithTimeout(r.Context(), internal.RedisTimeout)
	if err := h.store.SaveResponse(redisCtx, &internal.Response{
		Status:    "pending",
		RequestID: reqID,
	}, internal.StatusTTL); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "failed to initialize request"})
		return
	}
	redisCancel()

	msg := &internal.KafkaMessage{
		RequestID:  reqID,
		Raw:        raw,
		ReceivedAt: time.Now().Unix(),
	}

	select {
	case h.kafkaCh <- msg:
	default:
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
	h.cancel()       // Cancel parent context
	close(h.kafkaCh) // Close kafka channel to stop workers
}
