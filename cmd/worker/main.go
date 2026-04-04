package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"drpc_proxy.com/internal"
	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/redis"
	"drpc_proxy.com/internal/worker"
)

var (
	kafkaAddr string
	redisAddr string
	groupID   string
	workers   int
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "drpc-worker",
		Short: "Kafka worker for processing JSON-RPC requests",
	}

	rootCmd.PersistentFlags().StringVar(&kafkaAddr, "kafka", "kafka:9092", "Kafka broker address")
	rootCmd.PersistentFlags().StringVar(&redisAddr, "redis", "redis:6379", "Redis address")

	workerCmd := &cobra.Command{
		Use:   "run",
		Short: "Start the Kafka worker",
		Run:   runWorker,
	}

	workerCmd.Flags().StringVar(&groupID, "group", "rpc-workers", "Kafka consumer group ID")
	workerCmd.Flags().IntVar(&workers, "workers", 20, "Number of worker goroutines")

	rootCmd.AddCommand(workerCmd)

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runWorker(cmd *cobra.Command, args []string) {
	consumer, err := kafka.NewConsumer(kafkaAddr, groupID, workers)
	if err != nil {
		log.Fatal(err)
	}

	store := redis.NewStore(redisAddr)
	processor := worker.NewProcessor(store)

	// Setup graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Start consumer in a goroutine
	go func() {
		log.Printf("Worker started (kafka=%s group=%s workers=%d redis=%s)",
			kafkaAddr, groupID, workers, redisAddr)
		consumer.Consume(processor.Process)
	}()

	// Wait for interrupt signal
	<-sigCh
	log.Println("Shutdown signal received, gracefully shutting down...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), internal.ShutdownTimeout)
	defer cancel()

	// Close processor (cancels context)
	processor.Close()

	// Close Kafka consumer
	if err := consumer.Close(ctx); err != nil {
		log.Printf("Kafka consumer close error: %v", err)
	}

	// Close Redis store
	if err := store.Close(ctx); err != nil {
		log.Printf("Redis store close error: %v", err)
	}

	log.Println("Worker shutdown complete")
}
