# DRPC Proxy

A high-throughput, async JSON-RPC proxy built in Go that decouples HTTP clients from upstream blockchain RPC nodes using Apache Kafka and Redis.

---

## Table of Contents

1. [Design](#1-design)
   - [Architecture Overview](#architecture-overview)
   - [C1 — System Context](#c1--system-context)
   - [C2 — Container Diagram](#c2--container-diagram)
   - [Key Design Decisions](#key-design-decisions)
2. [Setup](#2-setup)
   - [Prerequisites](#prerequisites)
   - [Local Environment (LocalStack)](#local-environment-localstack)
   - [Running the Full Stack](#running-the-full-stack)
3. [Load Test](#3-load-test)
   - [Load Test](#load-test)
   - [QPS Capacity](#qps-capacity)

---

## 1. Design

### Architecture Overview

DRPC Proxy uses with a **fire-and-forget + poll** pattern to delegate the JSON-RPC request:

1. The client **POSTs** a JSON-RPC request to the proxy and immediately receives a `request_id`.
2. The proxy enqueues the request into **Kafka** and saves a `pending` status in **Redis** — the HTTP response is returned in < 5ms.
3. A **Worker** pool consumes from Kafka, calls the upstream RPC node, and writes the result back to Redis.
4. The client **polls** `GET /result?request_id=<id>` until the status is `completed` or `failed`.

This completely decouples the ingest rate from the upstream processing rate, making the system resilient to upstream slowdowns without dropping requests.

---

### C1 — System Context

![](doc/Drawing%202026-04-05%2018.13.53.excalidraw.png)

A mobile client (or any HTTP consumer) interacts with the **dRPC_Proxy** system via two operations: `POST /rpc` to submit a JSON-RPC request, and `GET /result` to retrieve the response. Inside the system boundary, multiple **Proxy Instances** receive inbound traffic and pass messages to multiple **Worker Instances**, which in turn call the **Upstream** blockchain RPC node. The client never waits for the upstream call — the proxy returns a `request_id` immediately and the client polls separately.

---

### C2 — Container Diagram

![](doc/Drawing%202026-04-05%2018.32.53.excalidraw.png)

The system is split across two runtime environments:

- **LocalStack** (orange boundary) simulates the AWS infrastructure locally. The **Load Balancer** (ALB) distributes incoming `POST/GET` traffic across multiple **Proxy Instances** (ECS Fargate tasks). A separate set of **Worker Instances** (ECS Fargate tasks) processes the queued requests and call the **Upstream** RPC node.
- **Docker Compose** (purple boundary) hosts the supporting infrastructure. **Kafka** (multiple brokers, 3 partitions) acts as the durable message queue between proxy and workers. **Redis** serves as the shared state store for both sides.

The data flow is: Proxy → **Produce** to Kafka; Worker ← **Consume** from Kafka; Proxy ↔ Redis **Get/Set** (read status, write pending/queued); Worker → Redis **Set** (write completed result).

---

### Key Design Decisions

| Decision | Rationale |
|---|---|
| **Kafka as request queue** | Durable, replayable, absorbs bursts; back-pressure is natural (Kafka lag) |
| **Redis for result storage** | Sub-millisecond reads; TTL-based cleanup (5–10 min) avoids manual GC |
| **Fire-and-forget + poll** | HTTP connection holds for < 5ms regardless of upstream latency |
| **Semaphore on proxy** | Hard cap on concurrent HTTP handlers prevents runaway goroutine growth |
| **Manual Kafka commit** | Offset only advances after Redis write succeeds — no silent result loss |
| **DLQ on permanent failure** | Failed messages are preserved for inspection/replay rather than dropped |
| **Mock mode on worker** | Workers can run with a configurable mock processor (tunable latency via `--mock-min-latency` / `--mock-max-latency`) that mimics upstream behaviour without real network calls, enabling reproducible load testing in the local environment |
| **Batch upstream RPC** | Workers accumulate up to `batchSize` (default 50) Kafka messages and send a single JSON-RPC batch POST per round-trip, reducing upstream connections and amortising per-request overhead by up to 50× |

---

## 2. Setup

### Prerequisites

This repository ships with a [Dev Container](https://containers.dev/) (`.devcontainer/`). Opening the project in **VS Code** with the [Dev Containers extension](https://marketplace.visualstudio.com/items?itemName=ms-vscode-remote.remote-containers) automatically provisions a fully configured development environment — Go, Terraform, Task, AWS CLI, and all other tooling are pre-installed inside the container.

**The only things required on the host machine are:**

| Tool | Purpose | Install |
|---|---|---|
| **Docker Engine** | Runs the dev container; the Docker socket is mounted inside so all `docker` / `docker compose` commands run from within the container | https://docs.docker.com/get-docker |
| **VS Code** | IDE with Dev Containers support | https://code.visualstudio.com |

> You will also need a **LocalStack Pro auth token** (`LOCALSTACK_AUTH_TOKEN`) for ECS Fargate support. Add it to a `.env` file or export it in your shell before opening the dev container.

> **Why LocalStack?**  
> We use [LocalStack Pro](https://localstack.cloud) to simulate the full AWS deployment — ECS Fargate tasks, ECR image registry, ALB load balancer, CloudWatch Logs, Secrets Manager, and IAM — entirely on your laptop. The application code and Terraform configuration are identical between local and production; only the provider endpoint changes (`http://localstack:4566`).

---

### Running the Full Stack

#### Step 1 — Start infrastructure

```bash
task docker:up
```

Starts Kafka (KRaft mode), Redis, and LocalStack in Docker Compose, then joins the devcontainer to the shared Docker network.

#### Step 2 — Initialise Terraform

```bash
task local:init
```

Downloads the AWS provider (runs once).

#### Step 3 — Deploy ECS services (mock mode)

```bash
task local:apply:mock
```

Provisions ECR repositories, ECS cluster, task definitions, services (3 proxy + 3 worker tasks), ALB, and security groups via Terraform. Workers run in **mock mode** — no real upstream calls, simulated 100–200ms latency.

For real mode (workers forward all requests to the upstream RPC node at `polygon-amoy.drpc.org`):

```bash
task local:apply
```

#### Step 4 — Build and push images to LocalStack ECR

```bash
task local:push
```

Builds `drpc-proxy` and `drpc-worker` Docker images and pushes them to the LocalStack ECR registry created in the previous step. ECS will pull the images on the next deployment.

#### Step 5 — Start monitoring

```bash
task monitoring:up
```

Starts Prometheus (port `9090`) and Grafana (port `3000`). Prometheus uses Docker Service Discovery to automatically scrape all ECS containers. Grafana is pre-provisioned with a dashboard showing request rate, Kafka throughput, worker processing rate, and latency percentiles.

#### Step 6 — Verify

There are three ways to verify the stack:

**Manual test** — sends a single `eth_blockNumber` request through the proxy and polls until the result is available:

```bash
task test:manual
```

**End-to-end test** — runs automated e2e assertions covering the full request/result lifecycle:

```bash
task test:e2e
```

**Load test** — simulates 100 concurrent users sending requests continuously (see [§3 Load Test](#3-load-test) for details):

```bash
task test:load
```

#### Tear down

```bash
task local:destroy
task docker:down
```

---

### Directory Structure

```
.
├── cmd/
│   ├── proxy/
│   │   └── main.go                 # Proxy HTTP server entrypoint
│   └── worker/
│       └── main.go                 # Kafka consumer worker entrypoint
├── internal/
│   ├── const.go                    # All tuneable constants
│   ├── message.go                  # Shared message types
│   ├── kafka/
│   │   ├── consumer.go             # Kafka reader, job dispatch, DLQ, offset commit
│   │   ├── consumer_test.go
│   │   ├── producer.go             # Kafka writer (batched, Snappy)
│   │   └── producer_test.go
│   ├── metrics/
│   │   └── metrics.go              # Prometheus metric definitions
│   ├── proxy/
│   │   ├── handler.go              # HTTP handler, semaphore, kafkaCh
│   │   └── handler_test.go
│   ├── redis/
│   │   ├── store.go                # Result store (Get/Set with TTL)
│   │   └── store_test.go
│   └── worker/
│       ├── handler.go              # Real upstream processor
│       ├── handler_test.go
│       ├── mock_handler.go         # Mock processor (configurable latency)
│       └── mock_handler_test.go
├── terraform/
│   ├── main.tf
│   ├── variables.tf
│   ├── outputs.tf
│   ├── locals.tf
│   ├── localstack.tfvars
│   └── modules/
│       ├── ecr/                    # ECR image registry
│       ├── ecs/                    # ECS cluster, task definitions, services, ALB
│       ├── elasticache/            # Redis (ElastiCache)
│       ├── iam/                    # Task execution roles
│       ├── msk/                    # Kafka (MSK)
│       ├── networking/             # VPC, subnets, security groups
│       └── secrets/                # Secrets Manager entries
├── tests/
│   ├── e2e/
│   │   └── e2e_test.go             # End-to-end request/result lifecycle tests
│   └── load/
│       └── load_test.go            # Continuous load test (build tag: load)
├── deploy/
│   ├── ecs/
│   │   ├── task-proxy.json         # ECS task definition (proxy)
│   │   └── task-worker.json        # ECS task definition (worker)
│   ├── grafana/
│   │   └── provisioning/
│   │       ├── dashboards/         # Grafana dashboard JSON + config
│   │       └── datasources/        # Prometheus datasource config
│   ├── localstack/
│   │   └── bootstrap.sh            # LocalStack init script
│   └── prometheus.yml              # Prometheus scrape config (Docker SD)
├── doc/                            # Architecture diagrams
├── .devcontainer/                  # Dev container definition (Dockerfile, post-create hooks)
├── Dockerfile.proxy
├── Dockerfile.worker
├── docker-compose.yml
├── Taskfile.yml
├── go.mod
└── go.sum
```

---

## 3. Load Test

### Load Test

The load test (`tests/load/load_test.go`, build tag `load`) targets the proxy ingest path (`POST /rpc`) using 500 concurrent user goroutines and a token-bucket rate limiter set to **5,000 req/s** by default. Every 10 seconds a stats window is printed with request counts, error rate, and a latency histogram (p50/p90/p95/p99/max).

```bash
# Default: 500 users, 5,000 req/s, runs until Ctrl+C
task test:load

# Custom parameters
task test:load USERS=200 RATE=2000               # ~2,000 req/s (light)
task test:load USERS=500 RATE=10000              # ~10,000 req/s (stress)
task test:load USERS=500 RATE=5000 DURATION=5m  # fixed duration

# Override target (bypass ALB, hit single proxy directly)
PROXY_ADDR=http://172.19.0.5:8545 task test:load
```

The task auto-detects the ALB (`drpc-local-alb.elb.localhost.localstack.cloud`) if available, otherwise falls back to a single proxy container IP.

### QPS Capacity

Capacity is governed by the worker processing pipeline. Because workers send **batch JSON-RPC POSTs** (up to `batchSize` messages per upstream round-trip), throughput scales with batch size as well as parallelism:

$$\text{max msg/s} = \frac{\text{worker tasks} \times \text{goroutines per task} \times \text{batch size}}{\text{avg upstream latency}}$$

**Worker tasks** = `worker_desired_count` (ECS task count, default 3 — one per Kafka partition). **Goroutines per task** = `worker_goroutines` (default 50).

| Scenario | Worker Tasks (`worker_desired_count`) | Goroutines per Task (`worker_goroutines`) | Batch Size | Avg Latency | Capacity |
|---|---|---|---|---|---|
| **Local mock (default)** | 3 | 50 | 50 | 150ms | **~50,000 msg/s** |
| Local mock (fast) | 3 | 50 | 50 | 30ms | ~250,000 msg/s |
| Production (real upstream) | 3 | 50 | 50 | 300ms | ~25,000 msg/s |
| Scale-out | 6 | 50 | 50 | 150ms | ~100,000 msg/s |

> **Kafka partitions = ceiling on worker tasks.** The topic has 3 partitions; adding a 4th worker container gains nothing. To scale beyond the default, increase partitions and `worker_desired_count` together.

> **The numbers above are theoretical maximums for the batch pipeline.** The local load test targets 5,000 QPS — not because that is the ceiling, but because several local factors constrain how high you can practically push it: a single Kafka broker shared across all 3 partitions, synchronous produce ACK latency (~20–50ms over Docker networking vs ~2ms on real MSK), a single ALB, and ECS containers running on shared host resources with limited CPU and memory. The actual ceiling depends on which of these bottlenecks you hit first. These constraints do not exist in a real AWS deployment.

**Default load test vs capacity:**

| Parameter | Value |
|---|---|
| Users | 500 |
| Rate | 5,000 req/s (token-bucket) |
| Ingest rate | ~5,000 req/s |
| Worker capacity (150ms latency, batch 50) | ~50,000 msg/s |
| Kafka lag (steady state at 5k) | ≈ 0 |

**Proxy ingest path latency** (POST `/rpc`, no upstream involved):

| Percentile | Observed |
|---|---|
| p50 | < 5ms |
| p95 | < 15ms |
| p99 | < 30ms |

The proxy itself is not the bottleneck. The semaphore (`proxy_max_concurrent = 10000` slots) and the `kafkaCh` buffer (`maxConcurrent × 2` per instance) provide headroom for burst traffic well beyond the worker consumption rate, with Kafka acting as the durable overflow buffer.

#### Why 5,000 RPS is a Local Test Scenario, Not a System Limit

`RATE=5000` is chosen to exercise the system at a meaningful load level within LocalStack's constraints. It is **not** the throughput ceiling:

- **LocalStack Kafka produce latency** is 20–50ms per synchronous ACK over Docker networking. On a real AWS MSK cluster the same call takes ~2ms. Each `proxy_kafka_workers` goroutine can therefore flush 10–25× fewer messages per second locally than in production.
- **Single Kafka broker** means all 3 partitions share one node. Real MSK distributes partitions across brokers.
- The `drpc_proxy_queue_full_total` Grafana metric shows how often the `kafkaCh` buffer was full and a request was dropped (HTTP 503). At 5k RPS with `proxy_kafka_workers=128` this should remain zero. If you see drops, increase `proxy_kafka_workers` further.

#### Tuning Parameters

Adjust these knobs in `terraform/localstack.tfvars` to push throughput beyond 5,000 RPS:

| Parameter | Default | Effect |
|---|---|---|
| `proxy_kafka_workers` | `128` | Producer goroutines draining `kafkaCh` into Kafka; set to at least `targetRPS × produceLatencySeconds` |
| `proxy_max_concurrent` | `10000` | Semaphore slots on the proxy; must be ≥ peak in-flight requests |
| `worker_goroutines` | `50` | Parallel batch upstream calls per worker task |
| `worker_desired_count` / `proxy_desired_count` | `3` | Horizontal scale — increase Kafka partitions to match |
| `--batch-size` / `BATCH_SIZE` env var | `50` | Messages per upstream batch POST; larger batches = fewer round-trips |
| `RATE` (load test) | `5000` | Token-bucket target RPS for the load generator |
| `USERS` (load test) | `500` | Concurrent user goroutines (each holds one in-flight request at a time) |

[localtest](https://github.com/user-attachments/assets/daf033f3-0992-49d4-9002-a7d87422b546)