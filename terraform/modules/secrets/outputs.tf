output "kafka_secret_arn" {
  value = aws_secretsmanager_secret.kafka.arn
}

output "redis_secret_arn" {
  value = aws_secretsmanager_secret.redis.arn
}
