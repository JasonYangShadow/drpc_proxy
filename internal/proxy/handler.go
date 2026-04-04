package proxy

import (
	"context"
	"net/http"
	"sync"
	"time"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/redis"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

const (
	defaultBufSize = 64 * 1024 // 64KB fixed buffer
	kafkaTimeout   = 5 * time.Second
	statusTTL      = 5 * time.Minute
	redisTimeout   = 2 * time.Second
)

type ErrorResponse struct {
	Error string `json:"error"`
}

type Response struct {
	Status    string      `json:"status,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
	Error     string      `json:"error,omitempty"`
	Result    interface{} `json:"result,omitempty"`
}

func (r *Response) Reset() {
	r.Status = ""
	r.RequestID = ""
	r.Error = ""
	r.Result = nil
}

type Handler struct {
	producer  *kafka.Producer
	store     *redis.Store
	semaphore chan struct{}

	kafkaCh   chan *internal.KafkaMessage
	onceStart sync.Once
}

func NewHandler(p *kafka.Producer, s *redis.Store, maxConcurrent int, kafkaWorkers int) *Handler {
	if maxConcurrent <= 0 {
		maxConcurrent = 1000
	}
	if kafkaWorkers <= 0 {
		kafkaWorkers = 32
	}

	h := &Handler{
		producer:  p,
		store:     s,
		semaphore: make(chan struct{}, maxConcurrent),
		kafkaCh:   make(chan *internal.KafkaMessage, maxConcurrent*2),
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
	buf := make([]byte, defaultBufSize)
	bgCtx := context.Background()

	for msg := range h.kafkaCh {
		b, err := sonic.Marshal(msg)
		if err != nil {
			redisCtx, cancel := context.WithTimeout(bgCtx, redisTimeout)
			_ = h.store.SetStatus(redisCtx, msg.RequestID, "failed: marshal error", statusTTL)
			cancel()
			continue
		}

		if len(b) > len(buf) {
			redisCtx, cancel := context.WithTimeout(bgCtx, redisTimeout)
			_ = h.store.SetStatus(redisCtx, msg.RequestID, "failed: payload too large > 64KB", statusTTL)
			cancel()
			continue
		}

		n := copy(buf, b)

		deadline := time.Now().Add(kafkaTimeout)
		kafkaCtx, kcancel := context.WithDeadline(bgCtx, deadline)
		err = h.producer.SendWithContext(kafkaCtx, msg.RequestID, buf[:n])
		kcancel()

		redisCtx, rcancel := context.WithTimeout(bgCtx, redisTimeout)
		if err != nil {
			_ = h.store.SetStatus(redisCtx, msg.RequestID, "failed: "+err.Error(), statusTTL)
		} else {
			_ = h.store.SetStatus(redisCtx, msg.RequestID, "queued", statusTTL)
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

	var req internal.RPCRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "invalid JSON-RPC request"})
		return
	}

	if err := req.Validate(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: err.Error()})
		return
	}

	reqID := uuid.New().String()

	// Set initial pending status in Redis
	redisCtx, redisCancel := context.WithTimeout(r.Context(), redisTimeout)
	defer redisCancel()

	if err := h.store.SetStatus(redisCtx, reqID, "pending", statusTTL); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "failed to initialize request"})
		return
	}

	msg := &internal.KafkaMessage{
		RequestID:  reqID,
		RPCRequest: req,
		ReceivedAt: time.Now().Unix(),
	}

	select {
	case h.kafkaCh <- msg:
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "kafka queue full, please retry later"})
		return
	}

	resp := Response{
		Status:    "queued",
		RequestID: reqID,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(&resp)
}

func (h *Handler) HandleResult(w http.ResponseWriter, r *http.Request) {
	requestID := r.URL.Query().Get("request_id")
	if requestID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "request_id parameter is required"})
		return
	}

	redisCtx, redisCancel := context.WithTimeout(r.Context(), redisTimeout)
	defer redisCancel()

	// Check status first using strong-typed struct
	st, statusErr := h.store.GetStatus(redisCtx, requestID)
	if statusErr == nil && st != nil {
		// Status exists - check if it's failed or pending
		if st.Status == "pending" {
			w.WriteHeader(http.StatusAccepted)
			_ = sonic.ConfigDefault.NewEncoder(w).Encode(Response{
				Status:    "pending",
				RequestID: requestID,
			})
			return
		}
		if st.Status == "queued" || st.Status == "processing" {
			w.WriteHeader(http.StatusAccepted)
			_ = sonic.ConfigDefault.NewEncoder(w).Encode(Response{
				Status:    st.Status,
				RequestID: requestID,
			})
			return
		}
		if len(st.Status) > 7 && st.Status[:7] == "failed:" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = sonic.ConfigDefault.NewEncoder(w).Encode(Response{
				Status:    "failed",
				RequestID: requestID,
				Error:     st.Status[8:], // Remove "failed: " prefix
			})
			return
		}
	}

	// Try to get the actual result
	result, err := h.store.GetWithContext(redisCtx, requestID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(ErrorResponse{Error: "result not found or not ready yet"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(result)
}
