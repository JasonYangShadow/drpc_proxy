# ── General ───────────────────────────────────────────────────────────────────

variable "aws_region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "us-east-1"
}

variable "environment" {
  description = "Deployment environment name (e.g. local, staging, prod)."
  type        = string
  default     = "prod"
}

variable "project" {
  description = "Project name used as a prefix for all resources."
  type        = string
  default     = "drpc"
}

# ── LocalStack ────────────────────────────────────────────────────────────────

variable "localstack_endpoint" {
  description = "LocalStack endpoint URL. Set to 'http://localhost:4566' to target LocalStack instead of real AWS. Leave empty for real AWS."
  type        = string
  default     = ""
}

# ── Networking ────────────────────────────────────────────────────────────────

variable "vpc_cidr" {
  description = "CIDR block for the VPC."
  type        = string
  default     = "10.0.0.0/16"
}

variable "availability_zones" {
  description = "List of availability zones to use."
  type        = list(string)
  default     = ["us-east-1a", "us-east-1b"]
}

# ── Images ────────────────────────────────────────────────────────────────────

variable "image_tag" {
  description = "Docker image tag to deploy (e.g. 'latest' or a git SHA)."
  type        = string
  default     = "latest"
}

# ── Proxy ─────────────────────────────────────────────────────────────────────

variable "proxy_cpu" {
  description = "Fargate CPU units for the proxy task (256, 512, 1024, 2048, 4096)."
  type        = number
  default     = 512
}

variable "proxy_memory" {
  description = "Fargate memory (MiB) for the proxy task."
  type        = number
  default     = 1024
}

variable "proxy_desired_count" {
  description = "Number of proxy Fargate tasks to run."
  type        = number
  default     = 3
}


variable "proxy_max_concurrent" {
  description = "Max concurrent in-flight requests per proxy instance."
  type        = number
  default     = 20000
}

variable "proxy_kafka_workers" {
  description = "Number of Kafka producer worker goroutines per proxy instance."
  type        = number
  default     = 256
}

# ── Worker ────────────────────────────────────────────────────────────────────

variable "worker_cpu" {
  description = "Fargate CPU units for the worker task."
  type        = number
  default     = 1024
}

variable "worker_memory" {
  description = "Fargate memory (MiB) for the worker task."
  type        = number
  default     = 2048
}

variable "worker_desired_count" {
  description = "Number of worker Fargate tasks to run."
  type        = number
  default     = 3
}

variable "worker_goroutines" {
  description = "Number of consumer goroutines per worker task."
  type        = number
  default     = 20
}

variable "worker_mock" {
  description = "Run the worker in mock mode (no real upstream calls). Useful for load testing."
  type        = bool
  default     = false
}

variable "worker_mock_min_latency" {
  description = "Mock processor minimum simulated latency (e.g. 10ms, 0ms)."
  type        = string
  default     = "10ms"
}

variable "worker_mock_max_latency" {
  description = "Mock processor maximum simulated latency (e.g. 50ms)."
  type        = string
  default     = "50ms"
}

# ── MSK (Kafka) ───────────────────────────────────────────────────────────────

variable "kafka_version" {
  description = "Apache Kafka version for MSK."
  type        = string
  default     = "3.6.0"
}

variable "msk_instance_type" {
  description = "MSK broker instance type."
  type        = string
  default     = "kafka.t3.small"
}

variable "msk_broker_count" {
  description = "Number of MSK broker nodes (must be a multiple of the number of AZs)."
  type        = number
  default     = 2
}

# ── ElastiCache (Redis) ───────────────────────────────────────────────────────

variable "elasticache_node_type" {
  description = "ElastiCache Redis node type."
  type        = string
  default     = "cache.t3.micro"
}
