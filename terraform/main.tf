terraform {
  required_version = ">= 1.14"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # Uncomment to use S3 remote state for real deployments:
  # backend "s3" {
  #   bucket         = "my-tf-state"
  #   key            = "drpc-proxy/terraform.tfstate"
  #   region         = "us-east-1"
  #   dynamodb_table = "tf-state-lock"
  #   encrypt        = true
  # }
}

provider "aws" {
  region = var.aws_region

  # LocalStack overrides — ignored when deploying to real AWS.
  dynamic "endpoints" {
    for_each = var.localstack_endpoint != "" ? [1] : []
    content {
      ec2            = var.localstack_endpoint
      ecs            = var.localstack_endpoint
      ecr            = var.localstack_endpoint
      iam            = var.localstack_endpoint
      logs           = var.localstack_endpoint
      secretsmanager = var.localstack_endpoint
      kafka          = var.localstack_endpoint
      elasticache    = var.localstack_endpoint
      elbv2          = var.localstack_endpoint
      s3             = var.localstack_endpoint
    }
  }

  # LocalStack accepts any credentials.
  skip_credentials_validation = var.localstack_endpoint != ""
  skip_requesting_account_id  = var.localstack_endpoint != ""
  skip_metadata_api_check     = var.localstack_endpoint != ""
  access_key                  = var.localstack_endpoint != "" ? "test" : null
  secret_key                  = var.localstack_endpoint != "" ? "test" : null
}

# ── Modules ───────────────────────────────────────────────────────────────────

module "networking" {
  source = "./modules/networking"

  name               = local.name
  vpc_cidr           = var.vpc_cidr
  availability_zones = var.availability_zones
  is_localstack      = local.is_localstack
}

module "iam" {
  source = "./modules/iam"
  name   = local.name
}

module "ecr" {
  source        = "./modules/ecr"
  name          = local.name
  is_localstack = local.is_localstack
}

module "secrets" {
  source        = "./modules/secrets"
  name          = local.name
  kafka_url     = module.msk.bootstrap_brokers
  redis_url     = module.elasticache.primary_endpoint
  is_localstack = local.is_localstack
}

module "msk" {
  source = "./modules/msk"

  name              = local.name
  vpc_id            = module.networking.vpc_id
  subnet_ids        = module.networking.private_subnet_ids
  security_group_id = module.networking.msk_security_group_id
  kafka_version     = var.kafka_version
  instance_type     = var.msk_instance_type
  number_of_brokers = var.msk_broker_count
  is_localstack     = local.is_localstack
}

module "elasticache" {
  source = "./modules/elasticache"

  name              = local.name
  vpc_id            = module.networking.vpc_id
  subnet_ids        = module.networking.private_subnet_ids
  security_group_id = module.networking.redis_security_group_id
  node_type         = var.elasticache_node_type
  is_localstack     = local.is_localstack
}

module "ecs" {
  source = "./modules/ecs"

  name                  = local.name
  aws_region            = var.aws_region
  vpc_id                = module.networking.vpc_id
  private_subnet_ids    = module.networking.private_subnet_ids
  alb_target_group_arn  = module.networking.alb_target_group_arn
  ecs_security_group_id = module.networking.ecs_security_group_id
  execution_role_arn    = module.iam.execution_role_arn
  task_role_arn         = module.iam.task_role_arn
  proxy_image           = "${module.ecr.proxy_repository_url}:${var.image_tag}"
  worker_image          = "${module.ecr.worker_repository_url}:${var.image_tag}"
  kafka_secret_arn      = module.secrets.kafka_secret_arn
  redis_secret_arn      = module.secrets.redis_secret_arn
  proxy_cpu             = var.proxy_cpu
  proxy_memory          = var.proxy_memory
  proxy_desired_count   = var.proxy_desired_count
  worker_cpu            = var.worker_cpu
  worker_memory         = var.worker_memory
  worker_desired_count  = var.worker_desired_count
  worker_goroutines     = var.worker_goroutines
  proxy_max_concurrent  = var.proxy_max_concurrent
  proxy_kafka_workers   = var.proxy_kafka_workers
  is_localstack         = local.is_localstack
}
