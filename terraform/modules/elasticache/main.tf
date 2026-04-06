resource "aws_elasticache_subnet_group" "main" {
  count      = var.is_localstack ? 0 : 1
  name       = "${var.name}-redis-subnets"
  subnet_ids = var.subnet_ids

  tags = { Name = "${var.name}-redis-subnets" }
}

resource "aws_elasticache_replication_group" "main" {
  count                      = var.is_localstack ? 0 : 1
  replication_group_id       = "${var.name}-redis"
  description                = "Redis for ${var.name}"
  engine                     = "redis"
  engine_version             = "7.1"
  node_type                  = var.node_type
  num_cache_clusters         = 1
  automatic_failover_enabled = false
  subnet_group_name          = aws_elasticache_subnet_group.main[0].name
  security_group_ids         = [var.security_group_id]
  at_rest_encryption_enabled = true
  transit_encryption_enabled = false # keep plaintext for simplicity; enable TLS for prod

  tags = { Name = "${var.name}-redis" }
}

locals {
  primary_endpoint = var.is_localstack ? "redis:6379" : "${aws_elasticache_replication_group.main[0].primary_endpoint_address}:6379"
}
