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

# Mock worker latency — realistic upstream RPC latency.
# Capacity: 3 containers × 50 goroutines / 150ms avg ≈ 1,000 msg/s
worker_mock_min_latency = "100ms"
worker_mock_max_latency = "200ms"

# Worker goroutines per container.
# With 10–50ms mock latency: 3 containers × 50 goroutines / 30ms avg ≈ 5,000 msg/s capacity.
# Kafka topic has 3 partitions so kafka-go will never use more than 3 goroutines for reading;
# the remaining goroutines drain the jobCh buffer in parallel.
worker_goroutines = 50

# MSK/ElastiCache are not scheduled by LocalStack community;
# these point to the docker-compose Kafka and Redis instead.
msk_broker_count = 1
