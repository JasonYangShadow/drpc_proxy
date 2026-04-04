package worker

import (
	"log"

	"drpc_proxy.com/internal/kafka"
	"drpc_proxy.com/internal/redis"
	"drpc_proxy.com/internal/worker"
)

func main() {
	consumer, err := kafka.NewConsumer("kafka:9092", "rpc-workers")
	if err != nil {
		log.Fatal(err)
	}

	store := redis.NewStore("redis:6379")

	processor := worker.NewProcessor(store)

	log.Println("Worker started")
	consumer.Consume("rpc_requests", processor.Process)
}
