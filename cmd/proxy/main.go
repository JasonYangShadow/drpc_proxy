package proxy

import (
	"log"
	"net/http"

	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/proxy"
	"drpc_proxy.com/internal/redis"
)

func main() {
	producer, err := kafka.NewProducer("kafka:9092")
	if err != nil {
		log.Fatal(err)
	}

	store := redis.NewStore("redis:6379")

	// Max 5000 concurrent requests
	handler := proxy.NewHandler(producer, store, 1000, 10)

	http.HandleFunc("/rpc", handler.HandleRPC)
	http.HandleFunc("/result/", handler.HandleResult)

	log.Println("Proxy running on :8545")
	log.Fatal(http.ListenAndServe(":8545", nil))
}
