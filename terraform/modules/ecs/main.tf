# ── Cluster ───────────────────────────────────────────────────────────────────

resource "aws_ecs_cluster" "main" {
  name = var.name

  setting {
    name  = "containerInsights"
    value = var.is_localstack ? "disabled" : "enabled"
  }

  tags = { Name = var.name }
}

# ── CloudWatch Log Groups ──────────────────────────────────────────────────────

resource "aws_cloudwatch_log_group" "proxy" {
  name              = "/ecs/${var.name}-proxy"
  retention_in_days = 30
  tags              = { Name = "${var.name}-proxy-logs" }
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/ecs/${var.name}-worker"
  retention_in_days = 30
  tags              = { Name = "${var.name}-worker-logs" }
}

# ── Task Definitions ──────────────────────────────────────────────────────────

resource "aws_ecs_task_definition" "proxy" {
  family                   = "${var.name}-proxy"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = tostring(var.proxy_cpu)
  memory                   = tostring(var.proxy_memory)
  execution_role_arn       = var.execution_role_arn
  task_role_arn            = var.task_role_arn

  container_definitions = jsonencode([{
    name      = "${var.name}-proxy"
    image     = var.proxy_image
    essential = true

    portMappings = [{
      containerPort = 8545
      hostPort      = 8545
      protocol      = "tcp"
    }]

    command = [
      "--port", "8545",
      "--max-concurrent", tostring(var.proxy_max_concurrent),
      "--kafka-workers", tostring(var.proxy_kafka_workers),
    ]

    secrets = [
      { name = "KAFKA_ADDR", valueFrom = var.kafka_secret_arn },
      { name = "REDIS_ADDR", valueFrom = var.redis_secret_arn },
    ]

    environment = [
      { name = "GOGC", value = "100" },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.proxy.name
        awslogs-region        = var.aws_region
        awslogs-stream-prefix = "ecs"
      }
    }

    healthCheck = {
      command     = ["CMD-SHELL", "wget -qO- http://localhost:8545/health || exit 1"]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 10
    }

    linuxParameters = { initProcessEnabled = true }
  }])

  tags = { Name = "${var.name}-proxy" }
}

resource "aws_ecs_task_definition" "worker" {
  family                   = "${var.name}-worker"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = tostring(var.worker_cpu)
  memory                   = tostring(var.worker_memory)
  execution_role_arn       = var.execution_role_arn
  task_role_arn            = var.task_role_arn

  container_definitions = jsonencode([{
    name      = "${var.name}-worker"
    image     = var.worker_image
    essential = true

    command = [
      "--goroutines", tostring(var.worker_goroutines),
    ]

    secrets = [
      { name = "KAFKA_ADDR", valueFrom = var.kafka_secret_arn },
      { name = "REDIS_ADDR", valueFrom = var.redis_secret_arn },
    ]

    environment = [
      { name = "GOGC", value = "100" },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.worker.name
        awslogs-region        = var.aws_region
        awslogs-stream-prefix = "ecs"
      }
    }

    linuxParameters = { initProcessEnabled = true }
  }])

  tags = { Name = "${var.name}-worker" }
}

# ── Services ──────────────────────────────────────────────────────────────────

resource "aws_ecs_service" "proxy" {
  name            = "${var.name}-proxy"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.proxy.arn
  desired_count   = var.proxy_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = var.private_subnet_ids
    security_groups = [var.ecs_security_group_id]
    # On LocalStack private subnets have no NAT, so assign a public IP when running locally.
    assign_public_ip = var.is_localstack
  }

  load_balancer {
    target_group_arn = var.alb_target_group_arn
    container_name   = "${var.name}-proxy"
    container_port   = 8545
  }

  # Allow Terraform to update the task definition without forcing a new service.
  lifecycle {
    ignore_changes = [task_definition]
  }

  tags = { Name = "${var.name}-proxy" }
}

resource "aws_ecs_service" "worker" {
  name            = "${var.name}-worker"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.worker.arn
  desired_count   = var.worker_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [var.ecs_security_group_id]
    assign_public_ip = var.is_localstack
  }

  lifecycle {
    ignore_changes = [task_definition]
  }

  tags = { Name = "${var.name}-worker" }
}
