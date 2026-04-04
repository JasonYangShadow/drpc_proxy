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
)

// upstreamError carries a non-retryable upstream error along with its response body
type upstreamError struct {
	statusCode int
	body       []byte
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("upstream error (status %d): %s", e.statusCode, e.body)
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

func (p *Processor) Process(msg []byte) ([]byte, error) {
	var m internal.KafkaMessage
	if err := sonic.Unmarshal(msg, &m); err != nil {
		return nil, err
	}

	// Use pre-marshaled RPC payload
	rpcPayload := m.Raw

	var respBody []byte
	var err error

	// retry upstream RPC
	for attempt := 1; attempt <= internal.UpstreamMaxRetries; attempt++ {
		respBody, err = p.callUpstream(rpcPayload)
		if err == nil {
			break
		}

		// Non-retryable upstream error — stop immediately
		var upErr *upstreamError
		if errors.As(err, &upErr) {
			return upErr.body, err
		}

		// Skip backoff on last attempt
		if attempt == internal.UpstreamMaxRetries {
			break
		}

		// Exponential backoff: 100ms, 200ms, 400ms... capped at 2s
		backoff := internal.UpstreamRetryInitialBackoff * (1 << (attempt - 1))
		if backoff > internal.UpstreamRetryMaxBackoff {
			backoff = internal.UpstreamRetryMaxBackoff
		}

		// Context-aware sleep: interrupted on shutdown
		select {
		case <-time.After(backoff):
		case <-p.ctx.Done():
			return nil, fmt.Errorf("context cancelled during retry backoff: %w", p.ctx.Err())
		}
	}

	if err != nil {
		return nil, err
	}

	return respBody, nil
}

func (p *Processor) callUpstream(raw []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), internal.UpstreamTimeout)
	defer cancel()

	url := p.upstreamURL
	if url == "" {
		url = internal.UpstreamURL
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// non-retryable 4xx errors — carry response body back to caller
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, internal.MaxUpstreamResponseSize))
		return nil, &upstreamError{statusCode: resp.StatusCode, body: body}
	}

	// retryable 5xx / unexpected status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, internal.MaxUpstreamResponseSize)
	return io.ReadAll(limited)
}

func (p *Processor) Close() {
	p.cancel() // Cancel parent context
}
