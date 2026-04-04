output "cluster_name" {
  value = aws_ecs_cluster.main.name
}

output "cluster_arn" {
  value = aws_ecs_cluster.main.arn
}

output "proxy_service_name" {
  value = aws_ecs_service.proxy.name
}

output "worker_service_name" {
  value = aws_ecs_service.worker.name
}
