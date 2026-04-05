package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ── Proxy ──────────────────────────────────────────────────────────────

	// ProxyRequestsTotal counts every HTTP request received at /rpc.
	ProxyRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "drpc_proxy_requests_total",
		Help: "Total number of RPC requests received by the proxy.",
	})

	// ProxyKafkaSentTotal counts messages successfully enqueued to Kafka.
	ProxyKafkaSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "drpc_proxy_kafka_sent_total",
		Help: "Total number of messages successfully sent to Kafka.",
	})

	// ProxyKafkaErrorsTotal counts Kafka send failures.
	ProxyKafkaErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "drpc_proxy_kafka_errors_total",
		Help: "Total number of Kafka send errors.",
	})

	// ── Worker ─────────────────────────────────────────────────────────────

	// WorkerMessagesProcessed counts messages processed, labelled by outcome.
	// status values: "success", "failure"
	WorkerMessagesProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "drpc_worker_messages_processed_total",
		Help: "Total number of Kafka messages processed by the worker.",
	}, []string{"status"})

	// WorkerProcessingDuration measures end-to-end message processing time
	// (Kafka read → upstream RPC → Redis save).
	WorkerProcessingDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "drpc_worker_processing_duration_seconds",
		Help:    "Time spent processing a single Kafka message (including upstream RPC call).",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	})

	// WorkerDLQSentTotal counts messages routed to the dead-letter queue.
	WorkerDLQSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "drpc_worker_dlq_sent_total",
		Help: "Total number of messages sent to the dead-letter queue.",
	})
)
