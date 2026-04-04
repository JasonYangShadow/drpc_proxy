# Deployment Manual

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Local Development](#local-development)
3. [Running the Full Stack Locally](#running-the-full-stack-locally)
4. [Infrastructure with LocalStack](#infrastructure-with-localstack)
5. [AWS Production Deployment](#aws-production-deployment)
6. [Networking Notes](#networking-notes)

---

## Prerequisites

| Tool | Minimum version | Purpose |
|------|----------------|---------|
| Docker + Docker Compose | 24+ | Running the local stack |
| [Task](https://taskfile.dev/installation/) | 3.x | Task runner (`go install github.com/go-task/task/v3/cmd/task@latest`) |
| Go | 1.25.8 | Building and testing |
| Terraform | 1.14+ | Infrastructure-as-code (pre-installed in dev container) |
| AWS CLI v2 | 2.x | Interacting with LocalStack and real AWS (pre-installed in dev container) |

All tools are pre-installed inside the VS Code dev container. If you are working outside the dev container, install them manually before proceeding.

---

## Local Development

Build, test, and lint without starting any external dependencies.

```bash
# Build both binaries to bin/
task build

# Run unit tests with the race detector
task test

# Run go vet across all packages
task lint

# Remove build artifacts
task clean
```

### Running tests

Unit tests use only in-process fakes — no Kafka, Redis, or Docker is required:

```bash
task test
```

End-to-end tests require the full local stack to be running (see below):

```bash
task dev:up
# wait for containers to become healthy, then:
go test -mod=vendor -v -tags e2e -timeout 120s ./tests/e2e/
```

---

## Running the Full Stack Locally

Docker Compose starts Kafka (KRaft), Redis, LocalStack, and the application containers. The tasks below automatically join the dev container to the compose network so you can reach containers by hostname (e.g. `drpc-proxy:8545`).

### Start everything

```bash
task dev:up
```

Builds Docker images (`drpc-proxy:local`, `drpc-worker:local`) and brings up all services:
`kafka`, `redis`, `localstack`, `proxy`, `worker`.

### Start with the mock worker (load testing)

```bash
task dev:up:mock
```

Starts `kafka`, `redis`, `proxy`, and `worker-mock`. The mock worker adds 100–200 ms artificial latency and has a 0.01% failure rate — no real upstream RPC node is needed.

### Start infrastructure only

```bash
task dev:up:infra
```

Starts only `kafka`, `redis`, and `localstack`. Useful when you want to run the Go binaries directly (`go run ./cmd/proxy`) against real-ish dependencies.

### Stop everything

```bash
task dev:down
```

### Follow logs

```bash
task dev:logs
```

Streams combined logs from all containers. Press `Ctrl-C` to stop.

### Check status

```bash
task dev:status
```

Shows running containers and lists the LocalStack resources that are currently provisioned (IAM roles, secrets, CloudWatch log groups).

---

### Sending a request

Once `dev:up` is healthy, the proxy listens on port **8545**. From inside the dev container (or after joining the compose network) use:

```bash
# Health check
curl -s http://drpc-proxy:8545/health
# Returns {"status":"ok"}

# Submit a JSON-RPC call
curl -s -X POST http://drpc-proxy:8545/rpc \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
# Returns {"id":"<uuid>"}

# Poll for the result using the returned id
curl -s http://drpc-proxy:8545/result/<uuid>
# Returns the upstream JSON-RPC response once the worker has processed it
```

From the host machine (outside the dev container), substitute `drpc-proxy` with `localhost`:

```
http://localhost:8545/rpc
http://localhost:8545/result/<uuid>
```

---

## Infrastructure with LocalStack

LocalStack simulates a subset of AWS services locally. The
Community edition (used here) supports **IAM**, **Secrets Manager**,
**CloudWatch Logs**, **STS**, and **S3**. VPC, ECR, and ECS are Pro-only and
are automatically skipped by the Terraform modules when
`is_localstack = true`.

### First-time setup

```bash
# 1. Start infrastructure containers (if not already running)
task dev:up:infra

# 2. Download Terraform providers (run once, or after upgrading providers)
task infra:init

# 3. Apply all Terraform modules against LocalStack
task infra:local
```

`infra:local` runs `terraform apply -var-file=localstack.tfvars -auto-approve` and creates:
- IAM execution roles for proxy and worker
- Secrets Manager entries (`drpc/kafka-addr`, `drpc/redis-addr`, `drpc/upstream-url`)
- CloudWatch log groups (`/ecs/drpc-proxy`, `/ecs/drpc-worker`)

### Verify

```bash
task dev:status
```

### Tear down

```bash
task infra:local:destroy
```

---

## AWS Production Deployment

### 1. Configure AWS credentials

```bash
export AWS_ACCESS_KEY_ID=<your-key>
export AWS_SECRET_ACCESS_KEY=<your-secret>
export AWS_DEFAULT_REGION=us-east-1
```

Or configure a named profile in `~/.aws/credentials` and export `AWS_PROFILE`.

### 2. Initialise Terraform

```bash
task infra:init
```

Downloads AWS provider plugins. Required once per machine / CI runner.

### 3. Review the plan

```bash
task infra:plan
```

Shows every resource that will be created in AWS before you commit to it. Review the output carefully.

The plan creates (in `us-east-1` by default):
- **VPC** with two public + two private subnets, NAT gateway
- **MSK** Kafka cluster (KRaft, `kafka.t3.small`)
- **ElastiCache Redis** cluster (`cache.t3.micro`)
- **ECR** repositories for `drpc-proxy` and `drpc-worker`
- **ECS Fargate** cluster with two services (`drpc-proxy`, `drpc-worker`)
- **IAM** execution + task roles
- **Secrets Manager** entries for runtime configuration
- **CloudWatch** log groups

### 4. Apply

```bash
task infra:apply
```

Terraform prompts for confirmation before making changes. Type `yes` when ready.

### 5. Push Docker images to ECR

After `infra:apply` completes, push the application images:

```bash
task infra:push
```

Builds both images from source and pushes them with the tag `latest`. To push a versioned tag:

```bash
task infra:push IMAGE_TAG=v1.2.3
```

Internally this:
1. Runs `aws ecr get-login-password` and authenticates Docker.
2. Builds `Dockerfile.proxy` → `<account>.dkr.ecr.<region>.amazonaws.com/drpc-proxy:<tag>`.
3. Builds `Dockerfile.worker` → `<account>.dkr.ecr.<region>.amazonaws.com/drpc-worker:<tag>`.
4. Pushes both images.

ECS will pick up the new image on the next service deployment (force a new deployment from the AWS console or with `aws ecs update-service --force-new-deployment`).

### 6. Destroy

```bash
task infra:destroy
```

Terraform prompts for confirmation. Destroys all AWS-managed resources in reverse dependency order.

> **Warning:** This deletes the MSK cluster, ElastiCache instance, ECS services, and all data stored in Secrets Manager. Ensure you have exported any data you need before destroying.

---

## Networking Notes

### Dev container ↔ Docker Compose

The VS Code dev container runs as a **sibling container** on the Docker host, not as a child of the compose network. This means:

- `localhost:9092`, `localhost:6379`, `localhost:4566`, etc. **do not work** from inside the dev container.
- Use container hostnames instead: `kafka:9092`, `redis:6379`, `localstack:4566`, `drpc-proxy:8545`.

The `dev:up*` tasks automatically run `docker network connect drpc_proxy_drpc <this-container>` so the dev container can reach compose services by hostname. This is idempotent — running it when already connected prints a message and exits cleanly.

### Port mapping for host access

The compose file exposes these ports to the Docker host, so you can reach services from your local machine (outside all containers) using `localhost`:

| Service | Host port |
|---------|-----------|
| proxy | 8545 |
| kafka | 9092 |
| redis | 6379 |
| localstack | 4566 |
