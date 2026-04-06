package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/worker"
)

var (
	kafkaAddr      string
	redisAddr      string
	groupID        string
	workers        int
	batchSize      int
	useMock        bool
	metricsPort    string
	mockMinLatency time.Duration
	mockMaxLatency time.Duration
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "drpc-worker",
		Short: "Kafka worker for processing JSON-RPC requests",
	}

	kafkaDefault := "kafka:9092"
	if v := os.Getenv("KAFKA_ADDR"); v != "" {
		kafkaDefault = v
	}
	redisDefault := "redis:6379"
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		redisDefault = v
	}

	rootCmd.PersistentFlags().StringVar(&kafkaAddr, "kafka", kafkaDefault, "Kafka broker address")
	rootCmd.PersistentFlags().StringVar(&redisAddr, "redis", redisDefault, "Redis address")

	workerCmd := &cobra.Command{
		Use:   "run",
		Short: "Start the Kafka worker",
		Run:   runWorker,
	}

	workerCmd.Flags().StringVar(&groupID, "group", "rpc-workers", "Kafka consumer group ID")
	workerCmd.Flags().IntVar(&workers, "workers", 50, "Number of worker goroutines")

	batchSizeDefault := internal.DefaultKafkaConsumerBatchSize
	if v := os.Getenv("BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			batchSizeDefault = n
		}
	}
	workerCmd.Flags().IntVar(&batchSize, "batch-size", batchSizeDefault, "Max messages per upstream batch request")

	workerCmd.Flags().BoolVar(&useMock, "mock", false, "Use mock processor (no real upstream calls, for load/mock testing)")
	workerCmd.Flags().StringVar(&metricsPort, "metrics-port", "2112", "Port to expose Prometheus /metrics endpoint")
	workerCmd.Flags().DurationVar(&mockMinLatency, "mock-min-latency", 100*time.Millisecond, "Mock processor minimum simulated latency")
	workerCmd.Flags().DurationVar(&mockMaxLatency, "mock-max-latency", 200*time.Millisecond, "Mock processor maximum simulated latency")

	rootCmd.AddCommand(workerCmd)

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runWorker(cmd *cobra.Command, args []string) {
	consumer, err := kafka.NewConsumer(kafkaAddr, groupID, redisAddr, workers, batchSize)
	if err != nil {
		log.Fatal(err)
	}

	var handler worker.MessageHandler
	if useMock {
		log.Printf("Mock mode: using MockProcessor (min-latency=%s max-latency=%s)", mockMinLatency, mockMaxLatency)
		m := worker.NewMockProcessor()
		m.MinLatency = mockMinLatency
		m.MaxLatency = mockMaxLatency
		handler = m
	} else {
		handler = worker.NewProcessor()
	}

	// Start Prometheus metrics server
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:    ":" + metricsPort,
		Handler: mux,
	}
	go func() {
		log.Printf("Metrics server listening on :%s", metricsPort)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	// Setup graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Start consumer in a goroutine
	go func() {
		log.Printf("Worker started (kafka=%s group=%s workers=%d redis=%s mock=%v)",
			kafkaAddr, groupID, workers, redisAddr, useMock)
		consumer.Consume(handler.ProcessBatch)
	}()

	// Wait for interrupt signal
	<-sigCh
	log.Println("Shutdown signal received, gracefully shutting down...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), internal.ShutdownTimeout)
	defer cancel()

	// Shut down metrics server so it stops accepting scrape requests
	if err := metricsSrv.Shutdown(ctx); err != nil {
		log.Printf("Metrics server shutdown error: %v", err)
	}

	// Close Kafka consumer first — drains in-flight workers before cancelling processor
	if err := consumer.Close(ctx); err != nil {
		log.Printf("Consumer close error: %v", err)
	}

	// Close processor after all workers have finished
	handler.Close()

	log.Println("Worker shutdown complete")
}
