variable "name" {
  type = string
}

variable "kafka_url" {
  type      = string
  sensitive = true
}

variable "redis_url" {
  type      = string
  sensitive = true
}

variable "is_localstack" {
  type    = bool
  default = false
}
