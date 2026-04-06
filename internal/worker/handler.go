package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"drpc_proxy.com/internal"
	"github.com/bytedance/sonic"
	kafka "github.com/segmentio/kafka-go"
)

// upstreamError carries a non-retryable upstream error along with its response body
type upstreamError struct {
	statusCode int
	body       []byte
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("upstream error (status %d): %s", e.statusCode, e.body)
}

// MessageHandler is the interface satisfied by both Processor and MockProcessor.
type MessageHandler interface {
	// ProcessBatch sends a batch of Kafka messages as a single JSON-RPC batch
	// request. msgs is keyed by request_id. Returns responses keyed by request_id.
	ProcessBatch(msgs map[string]kafka.Message) (map[string][]byte, error)
	Close()
}

var upstreamClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:          internal.UpstreamMaxIdleConns,
		MaxIdleConnsPerHost:   internal.UpstreamMaxIdleConnsPerHost,
		MaxConnsPerHost:       internal.UpstreamMaxConnsPerHost,
		IdleConnTimeout:       internal.UpstreamIdleConnTimeout,
		TLSHandshakeTimeout:   internal.UpstreamTLSHandshakeTimeout,
		ResponseHeaderTimeout: internal.UpstreamResponseHeaderTimeout,
	},
}

type Processor struct {
	ctx         context.Context
	cancel      context.CancelFunc
	upstreamURL string // overridable for tests; defaults to internal.UpstreamURL
}

func NewProcessor() *Processor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Processor{
		ctx:    ctx,
		cancel: cancel,
	}
}

// ProcessBatch sends msgs as a single JSON-RPC batch request to the upstream.
// msgs is keyed by request_id. Returns responses keyed by request_id;
// any error means the entire batch failed.
func (p *Processor) ProcessBatch(msgs map[string]kafka.Message) (map[string][]byte, error) {
	payloads := make([]sonic.NoCopyRawMessage, 0, len(msgs))
	for requestID, kafkaMsg := range msgs {
		var km internal.KafkaMessage
		if err := sonic.Unmarshal(kafkaMsg.Value, &km); err != nil {
			return nil, err
		}
		var rpcReq map[string]interface{}
		if err := sonic.Unmarshal(km.Raw, &rpcReq); err != nil {
			return nil, err
		}
		rpcReq["id"] = requestID
		overridden, err := sonic.Marshal(rpcReq)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, sonic.NoCopyRawMessage(overridden))
	}

	if len(payloads) == 0 {
		return nil, nil
	}

	var (
		rawResponses []sonic.NoCopyRawMessage
		batchErr     error
	)
	for attempt := 1; attempt <= internal.UpstreamMaxRetries; attempt++ {
		rawResponses, batchErr = p.callUpstreamBatch(payloads)
		if batchErr == nil {
			break
		}
		var upErr *upstreamError
		if errors.As(batchErr, &upErr) {
			break // non-retryable 4xx
		}
		if attempt == internal.UpstreamMaxRetries {
			break
		}
		backoff := internal.UpstreamRetryInitialBackoff * (1 << (attempt - 1))
		if backoff > internal.UpstreamRetryMaxBackoff {
			backoff = internal.UpstreamRetryMaxBackoff
		}
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-p.ctx.Done():
			t.Stop()
			return nil, fmt.Errorf("context cancelled during retry backoff: %w", p.ctx.Err())
		}
		t.Stop()
	}

	if batchErr != nil {
		return nil, batchErr
	}

	// Match each response back to its original message by the echoed request_id.
	results := make(map[string][]byte, len(rawResponses))
	for _, raw := range rawResponses {
		var h struct {
			ID string `json:"id"`
		}
		if err := sonic.Unmarshal(raw, &h); err != nil {
			continue
		}
		results[h.ID] = raw
	}

	for requestID := range msgs {
		if _, ok := results[requestID]; !ok {
			return nil, fmt.Errorf("missing response for request %s", requestID)
		}
	}

	return results, nil
}

func (p *Processor) callUpstreamBatch(payloads []sonic.NoCopyRawMessage) ([]sonic.NoCopyRawMessage, error) {
	body, err := sonic.Marshal(payloads)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal batch request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), internal.UpstreamTimeout)
	defer cancel()

	url := p.upstreamURL
	if url == "" {
		url = internal.UpstreamURL
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		// Non-retryable 4xx (except 429)
		b, _ := io.ReadAll(io.LimitReader(resp.Body, internal.MaxUpstreamResponseSize))
		return nil, &upstreamError{statusCode: resp.StatusCode, body: b}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Retryable: 429, 5xx; also catches unexpected 1xx/3xx as retryable
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, internal.MaxUpstreamBatchResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read batch response: %w", err)
	}
	var responses []sonic.NoCopyRawMessage
	if err := sonic.Unmarshal(b, &responses); err != nil {
		return nil, fmt.Errorf("failed to decode batch response: %w", err)
	}
	return responses, nil
}

func (p *Processor) Close() {
	p.cancel() // Cancel parent context
}
