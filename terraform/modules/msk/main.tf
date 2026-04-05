# MSK configuration (topic auto-creation on LocalStack is enabled by default).
# On real AWS we use a custom configuration to create topics via MSK.

resource "aws_msk_configuration" "main" {
  count          = var.is_localstack ? 0 : 1
  name           = "${var.name}-config"
  kafka_versions = [var.kafka_version]

  server_properties = <<EOF
auto.create.topics.enable=false
default.replication.factor=2
min.insync.replicas=1
num.io.threads=8
num.network.threads=5
num.partitions=3
num.replica.fetchers=2
replica.lag.time.max.ms=30000
socket.receive.buffer.bytes=102400
socket.request.max.bytes=104857600
socket.send.buffer.bytes=102400
unclean.leader.election.enable=true
zookeeper.connection.timeout.ms=2000
EOF
}

resource "aws_msk_cluster" "main" {
  count                  = var.is_localstack ? 0 : 1
  cluster_name           = "${var.name}-kafka"
  kafka_version          = var.kafka_version
  number_of_broker_nodes = var.number_of_brokers

  broker_node_group_info {
    instance_type   = var.instance_type
    client_subnets  = var.subnet_ids
    security_groups = [var.security_group_id]

    storage_info {
      ebs_storage_info {
        volume_size = 20
      }
    }
  }

  dynamic "configuration_info" {
    for_each = var.is_localstack ? [] : [1]
    content {
      arn      = aws_msk_configuration.main[0].arn
      revision = aws_msk_configuration.main[0].latest_revision
    }
  }



  encryption_info {
    encryption_in_transit {
      client_broker = "PLAINTEXT"
      in_cluster    = true
    }
  }

  tags = { Name = "${var.name}-kafka" }
}

# On LocalStack MSK is not fully supported; we surface a placeholder so secrets
# and ECS env vars are still populated consistently.
locals {
  bootstrap_brokers = var.is_localstack ? "kafka:9092" : aws_msk_cluster.main[0].bootstrap_brokers
}
