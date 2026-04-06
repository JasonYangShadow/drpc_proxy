locals {
  name          = "${var.project}-${var.environment}"
  is_localstack = var.localstack_endpoint != ""

  common_tags = {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}
