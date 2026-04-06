output "proxy_repository_url" {
  value = aws_ecr_repository.proxy.repository_url
}

output "worker_repository_url" {
  value = aws_ecr_repository.worker.repository_url
}
