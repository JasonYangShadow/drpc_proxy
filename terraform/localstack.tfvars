# LocalStack override — all values point to the local docker-compose stack.
# Usage: task tf:local:apply
#        terraform apply -var-file=localstack.tfvars

localstack_endpoint = "http://localstack:4566"
environment         = "local"
aws_region          = "us-east-1"

# Single-AZ is enough for local development.
availability_zones = ["us-east-1a"]

# Lightweight compute for local simulation.
proxy_cpu     = 256
proxy_memory  = 512
worker_cpu    = 512
worker_memory = 1024

proxy_desired_count  = 3
worker_desired_count = 3

# Kafka producer goroutines per proxy container.
# More workers = faster kafkaCh drain under LocalStack's slow sync-ack Kafka.
proxy_kafka_workers = 128

# Max concurrent in-flight requests per proxy instance (2× target QPS).
proxy_max_concurrent = 10000

# Mock worker latency — realistic upstream RPC latency.
worker_mock_min_latency = "100ms"
worker_mock_max_latency = "200ms"

# Worker goroutines per container.
# With batch size 50 and 150ms latency: 3 × 50 × 50 / 0.15 ≈ 50,000 msg/s theoretical capacity.
# Kafka topic has 3 partitions so kafka-go will never use more than 3 goroutines for reading;
# the remaining goroutines drain the jobCh buffer in parallel.
worker_goroutines = 50

# MSK/ElastiCache are not scheduled by LocalStack community;
# these point to the docker-compose Kafka and Redis instead.
msk_broker_count = 1
