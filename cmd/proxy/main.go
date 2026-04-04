package main

import (
	"log"
	"net/http"
	"time"

	"github.com/spf13/cobra"

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

	rootCmd.PersistentFlags().StringVar(&kafkaAddr, "kafka", "kafka:9092", "Kafka broker address")
	rootCmd.PersistentFlags().StringVar(&redisAddr, "redis", "redis:6379", "Redis address")

	// proxy command
	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the RPC proxy server",
		Run:   runProxy,
	}

	proxyCmd.Flags().StringVar(&port, "port", "8545", "HTTP listen port")
	proxyCmd.Flags().IntVar(&maxConc, "max-concurrent", 1000, "Max concurrent requests")
	proxyCmd.Flags().IntVar(&workers, "kafka-workers", 10, "Kafka worker count")

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

	http.HandleFunc("/rpc", handler.HandleRPC)
	http.HandleFunc("/result/", handler.HandleResult)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      nil,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("Proxy running on :%s (kafka=%s redis=%s)", port, kafkaAddr, redisAddr)
	log.Fatal(srv.ListenAndServe())
}
