output "vpc_id" {
  value = aws_vpc.main.id
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "ecs_security_group_id" {
  value = aws_security_group.ecs.id
}

output "msk_security_group_id" {
  value = aws_security_group.msk.id
}

output "redis_security_group_id" {
  value = aws_security_group.redis.id
}

output "alb_target_group_arn" {
  value = aws_lb_target_group.proxy.arn
}

output "alb_dns_name" {
  value = aws_lb.proxy.dns_name
}
