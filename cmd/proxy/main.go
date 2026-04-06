package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/proxy"
	"drpc_proxy.com/internal/redis"
)

var (
	kafkaAddr string
	redisAddr string
	port      string
	maxConc   int
	workers   int
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "drpc-proxy",
		Short: "High-performance JSON-RPC proxy for Polygon Amoy",
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

	// proxy command
	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the RPC proxy server",
		Run:   runProxy,
	}

	proxyCmd.Flags().StringVar(&port, "port", "8545", "HTTP listen port")
	proxyCmd.Flags().IntVar(&maxConc, "max-concurrent", internal.DefaultMaxConcurrent, "Max concurrent requests")
	proxyCmd.Flags().IntVar(&workers, "kafka-workers", internal.DefaultKafkaWorkers, "Kafka worker count")

	rootCmd.AddCommand(proxyCmd)

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runProxy(cmd *cobra.Command, args []string) {
	producer, err := kafka.NewProducer(kafkaAddr)
	if err != nil {
		log.Fatal(err)
	}

	store := redis.NewStore(redisAddr)

	handler := proxy.NewHandler(producer, store, maxConc, workers)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	http.HandleFunc("/rpc", handler.HandleRPC)
	http.HandleFunc("/result/", handler.HandleResult)
	http.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           nil,
		ReadTimeout:       internal.HttpReadTimeout,
		WriteTimeout:      internal.HttpWriteTimeout,
		IdleTimeout:       internal.HttpIdleTimeout,
		MaxHeaderBytes:    1 << 20, // 1MB
		ReadHeaderTimeout: internal.HttpReadTimeout,
	}

	// Setup graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		log.Printf("Proxy running on :%s (kafka=%s redis=%s)", port, kafkaAddr, redisAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	<-sigCh
	log.Println("Shutdown signal received, gracefully shutting down...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), internal.ShutdownTimeout)
	defer cancel()

	// Shutdown HTTP server
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	// Close handler (cancels context and stops kafka workers)
	handler.Close()

	// Close Kafka producer
	if err := producer.Close(ctx); err != nil {
		log.Printf("Kafka producer close error: %v", err)
	}

	// Close Redis store
	if err := store.Close(ctx); err != nil {
		log.Printf("Redis store close error: %v", err)
	}

	log.Println("Proxy shutdown complete")
}
