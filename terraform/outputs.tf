output "aws_region" {
  description = "AWS region resources are deployed into."
  value       = var.aws_region
}

output "alb_dns_name" {
  description = "DNS name of the Application Load Balancer (proxy endpoint)."
  value       = module.networking.alb_dns_name
}

output "proxy_ecr_repository_url" {
  description = "ECR URL for the proxy image."
  value       = module.ecr.proxy_repository_url
}

output "worker_ecr_repository_url" {
  description = "ECR URL for the worker image."
  value       = module.ecr.worker_repository_url
}

output "msk_bootstrap_brokers" {
  description = "MSK bootstrap broker connection string."
  value       = module.msk.bootstrap_brokers
  sensitive   = true
}

output "redis_primary_endpoint" {
  description = "ElastiCache Redis primary endpoint."
  value       = module.elasticache.primary_endpoint
  sensitive   = true
}

output "ecs_cluster_name" {
  description = "ECS cluster name."
  value       = module.ecs.cluster_name
}

output "vpc_id" {
  description = "VPC ID."
  value       = module.networking.vpc_id
}
