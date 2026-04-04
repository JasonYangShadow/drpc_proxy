package internal

import "time"

const (
	// Handler buffer size
	DefaultBufSize = 256 * 1024 // 256KB fixed buffer

	// Timeout configurations
	KafkaTimeout    = 5 * time.Second
	RedisTimeout    = 2 * time.Second
	UpstreamTimeout = 30 * time.Second

	// TTL configurations
	StatusTTL = 5 * time.Minute
	ResultTTL = 10 * time.Minute

	// upstream
	UpstreamURL             = "https://polygon-amoy.drpc.org"
	MaxUpstreamRequestSize  = 128 * 1024 // 128KB for the request size
	MaxUpstreamResponseSize = 256 * 1024 // 256KB for the response size
)
