package internal

import "time"

const (

	// Timeout configurations
	KafkaTimeout      = 5 * time.Second
	RedisTimeout      = 2 * time.Second
	UpstreamTimeout   = 3 * time.Second
	RedisDialTimeout  = 5 * time.Second
	RedisReadTimeout  = 3 * time.Second
	RedisWriteTimeout = 3 * time.Second

	HttpReadTimeout  = 5 * time.Second
	HttpWriteTimeout = 10 * time.Second
	HttpIdleTimeout  = 60 * time.Second

	// Graceful shutdown timeout
	ShutdownTimeout = 60 * time.Second

	// TTL configurations
	StatusTTL = 5 * time.Minute
	ResultTTL = 10 * time.Minute

	// upstream
	UpstreamURL                   = "https://polygon-amoy.drpc.org"
	MaxUpstreamRequestSize        = 128 * 1024 // 128KB for the request size
	MaxUpstreamResponseSize       = 256 * 1024 // 256KB for the response size
	UpstreamTLSHandshakeTimeout   = 2 * time.Second
	UpstreamResponseHeaderTimeout = 2 * time.Second

	// topic
	KafkaTopic = "rpc_requests"

	// Redis connection pool settings
	RedisPoolSize     = 100
	RedisMinIdleConns = 10
	RedisMaxRetries   = 3

	// HTTP client settings (for upstream RPC)
	UpstreamMaxIdleConns        = 1000
	UpstreamMaxIdleConnsPerHost = 1000
	UpstreamMaxConnsPerHost     = 1000
	UpstreamIdleConnTimeout     = 90 * time.Second

	// Proxy handler settings
	DefaultMaxConcurrent   = 1000
	DefaultKafkaWorkers    = 32
	KafkaChannelMultiplier = 2          // kafkaCh buffer = maxConcurrent * multiplier
	KafkaMessageMaxSize    = 256 * 1024 // 256KB fixed buffer

	// Kafka producer settings
	KafkaProducerBatchSize    = 100
	KafkaProducerBatchTimeout = 10 * time.Millisecond
	KafkaProducerMaxAttempts  = 3
	KafkaProducerReadTimeout  = 10 * time.Second
	KafkaProducerWriteTimeout = 10 * time.Second

	// Kafka consumer settings
	KafkaConsumerMinBytes          = 10e3 // 10KB
	KafkaConsumerMaxBytes          = 10e6 // 10MB
	KafkaConsumerMaxWait           = 50 * time.Millisecond
	KafkaConsumerReadLagInterval   = -1
	KafkaConsumerHeartbeatInterval = 3 * time.Second
	KafkaConsumerSessionTimeout    = 30 * time.Second
	KafkaConsumerRebalanceTimeout  = 30 * time.Second
	KafkaConsumerCommitInterval    = -1 // manual commit
	KafkaConsumerCommitTimeout     = 5 * time.Second
	KafkaConsumerJobChMultiplier   = 2 // jobCh buffer = workers * multiplier

	// retry settings
	UpstreamMaxRetries          = 3
	UpstreamRetryInitialBackoff = 100 * time.Millisecond // exponential base
	UpstreamRetryMaxBackoff     = 2 * time.Second        // backoff cap

	// dead letter queue settings
	KafkaDLQTopic        = "rpc_requests_dlq"
	KafkaDLQWriteTimeout = 5 * time.Second
)
