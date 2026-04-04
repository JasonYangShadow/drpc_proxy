variable "name" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  type = list(string)
}

variable "security_group_id" {
  type = string
}

variable "availability_zones" {
  type = list(string)
}

variable "number_of_brokers" {
  type    = number
  default = 2
}

variable "instance_type" {
  type    = string
  default = "kafka.t3.small"
}

variable "kafka_version" {
  type    = string
  default = "3.5.1"
}

variable "is_localstack" {
  type    = bool
  default = false
}
