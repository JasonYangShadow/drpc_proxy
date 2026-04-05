resource "aws_ecr_repository" "proxy" {
  name                 = "${var.name}-proxy"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = !var.is_localstack
  }

  tags = { Name = "${var.name}-proxy" }
}

resource "aws_ecr_repository" "worker" {
  name                 = "${var.name}-worker"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = !var.is_localstack
  }

  tags = { Name = "${var.name}-worker" }
}

locals {
  lifecycle_policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep last 10 images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 10
      }
      action = { type = "expire" }
    }]
  })
}

resource "aws_ecr_lifecycle_policy" "proxy" {
  repository = aws_ecr_repository.proxy.name
  policy     = local.lifecycle_policy
}

resource "aws_ecr_lifecycle_policy" "worker" {
  repository = aws_ecr_repository.worker.name
  policy     = local.lifecycle_policy
}
