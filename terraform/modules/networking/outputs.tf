output "vpc_id" {
  value = var.is_localstack ? "" : aws_vpc.main[0].id
}

output "public_subnet_ids" {
  value = var.is_localstack ? [] : aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = var.is_localstack ? [] : aws_subnet.private[*].id
}

output "ecs_security_group_id" {
  value = var.is_localstack ? "" : aws_security_group.ecs[0].id
}

output "msk_security_group_id" {
  value = var.is_localstack ? "" : aws_security_group.msk[0].id
}

output "redis_security_group_id" {
  value = var.is_localstack ? "" : aws_security_group.redis[0].id
}

output "alb_target_group_arn" {
  value = var.is_localstack ? "" : aws_lb_target_group.proxy[0].arn
}

output "alb_dns_name" {
  value = var.is_localstack ? "localhost:8545" : aws_lb.proxy[0].dns_name
}
