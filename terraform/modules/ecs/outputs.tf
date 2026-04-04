output "cluster_name" {
  value = var.is_localstack ? var.name : aws_ecs_cluster.main[0].name
}

output "cluster_arn" {
  value = var.is_localstack ? "" : aws_ecs_cluster.main[0].arn
}

output "proxy_service_name" {
  value = var.is_localstack ? "" : aws_ecs_service.proxy[0].name
}

output "worker_service_name" {
  value = var.is_localstack ? "" : aws_ecs_service.worker[0].name
}
