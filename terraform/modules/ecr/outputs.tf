output "proxy_repository_url" {
  value = var.is_localstack ? "000000000000.dkr.ecr.us-east-1.localhost.localstack.cloud:4566/${var.name}-proxy" : aws_ecr_repository.proxy[0].repository_url
}

output "worker_repository_url" {
  value = var.is_localstack ? "000000000000.dkr.ecr.us-east-1.localhost.localstack.cloud:4566/${var.name}-worker" : aws_ecr_repository.worker[0].repository_url
}
