variable "name" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "private_subnet_ids" {
  type = list(string)
}

variable "ecs_security_group_id" {
  type = string
}

variable "alb_target_group_arn" {
  type = string
}

variable "execution_role_arn" {
  type = string
}

variable "task_role_arn" {
  type = string
}

variable "proxy_image" {
  type = string
}

variable "worker_image" {
  type = string
}

variable "kafka_secret_arn" {
  type = string
}

variable "redis_secret_arn" {
  type = string
}

variable "proxy_cpu" {
  type    = number
  default = 512
}

variable "proxy_memory" {
  type    = number
  default = 1024
}

variable "proxy_desired_count" {
  type    = number
  default = 3
}

variable "worker_cpu" {
  type    = number
  default = 1024
}

variable "worker_memory" {
  type    = number
  default = 2048
}

variable "worker_desired_count" {
  type    = number
  default = 3
}

variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "proxy_max_concurrent" {
  type    = number
  default = 1000
}

variable "proxy_kafka_workers" {
  type    = number
  default = 128
}

variable "worker_goroutines" {
  type    = number
  default = 50
}

variable "worker_mock" {
  type    = bool
  default = false
}

variable "worker_mock_min_latency" {
  type    = string
  default = "100ms"
}

variable "worker_mock_max_latency" {
  type    = string
  default = "200ms"
}

variable "is_localstack" {
  type    = bool
  default = false
}
