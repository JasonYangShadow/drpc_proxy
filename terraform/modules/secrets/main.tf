resource "aws_secretsmanager_secret" "kafka" {
  name                    = "${var.name}-kafka-url"
  recovery_window_in_days = var.is_localstack ? 0 : 7

  tags = { Name = "${var.name}-kafka-url" }
}

resource "aws_secretsmanager_secret_version" "kafka" {
  secret_id     = aws_secretsmanager_secret.kafka.id
  secret_string = var.kafka_url
}

resource "aws_secretsmanager_secret" "redis" {
  name                    = "${var.name}-redis-url"
  recovery_window_in_days = var.is_localstack ? 0 : 7

  tags = { Name = "${var.name}-redis-url" }
}

resource "aws_secretsmanager_secret_version" "redis" {
  secret_id     = aws_secretsmanager_secret.redis.id
  secret_string = var.redis_url
}
