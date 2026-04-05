# DRPC Proxy

A high-throughput, async JSON-RPC proxy built in Go that decouples HTTP clients from upstream blockchain RPC nodes using Apache Kafka and Redis.

---

## Table of Contents

1. [Design](#1-design)
   - [Architecture Overview](#architecture-overview)
   - [C1 — System Context](#c1--system-context)
   - [C2 — Container Diagram](#c2--container-diagram)
   - [C3 — Component Diagram](#c3--component-diagram)
   - [Key Design Decisions](#key-design-decisions)
2. [Setup](#2-setup)
   - [Prerequisites](#prerequisites)
   - [Local Environment (LocalStack)](#local-environment-localstack)
   - [Running the Full Stack](#running-the-full-stack)
3. [Load Test](#3-load-test)
   - [Design](#load-test-design)
   - [Running](#running-the-load-test)
   - [QPS Capacity](#qps-capacity)

---

## 1. Design

### Architecture Overview

Traditional synchronous RPC proxies block an HTTP connection for the entire duration of the upstream call (100–500ms for blockchain nodes). Under load this exhausts connection pools and causes cascading timeouts.

DRPC Proxy solves this with a **fire-and-forget + poll** pattern:

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

#### Step 3 — Build and push images to LocalStack ECR

```bash
task local:push
```

Builds `drpc-proxy` and `drpc-worker` Docker images and pushes them to the LocalStack ECR registry.

#### Step 4 — Deploy ECS services (mock mode)

```bash
task local:apply:mock
```

Provisions ECS cluster, task definitions, services (3 proxy + 3 worker tasks), ALB, and security groups via Terraform. Workers run in **mock mode** — no real upstream calls, simulated 100–200ms latency.

For real mode (workers forward all requests to the upstream RPC node at `polygon-amoy.drpc.org`):

```bash
task local:apply
```

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

### Load Test Design

The load test (`tests/load/load_test.go`, build tag `load`) simulates **real user behaviour** rather than a bulk-sender:

- **N independent user goroutines** each loop continuously: send a request → sleep a random duration in `[0, GAP)` → repeat.
- This produces a **Poisson-like arrival process** (random inter-arrival times) rather than a synchronized burst, which is far more representative of real traffic.
- Requests rotate through 5 Ethereum JSON-RPC methods: `eth_blockNumber`, `eth_gasPrice`, `eth_chainId`, `eth_getBlockByNumber`, `net_version`.
- The test is **fire-and-forget** — it measures the proxy ingest path only (POST `/rpc` round-trip latency), not end-to-end result latency. This reflects the actual client experience: the proxy accepts immediately and the client polls separately.
- Every **10 seconds** a stats window is printed: total sent, ok/err counts, instant req/s, cumulative avg req/s, and a latency histogram (p50/p90/p95/p99/max).
- **Failures are expected and tracked.** A non-2xx response (e.g. when the proxy's semaphore is saturated, the `kafkaCh` buffer is full, or Kafka is temporarily unavailable) is counted as an error and reported in the stats. The error rate is a useful signal: a low rate indicates the system is handling load gracefully, while a rising error rate indicates back-pressure or a bottleneck that needs investigation.

```mermaid
sequenceDiagram
  participant U as User goroutine (×100)
  participant P as Proxy POST /rpc
  participant KCh as kafkaCh (buffer)
  participant KW as Kafka Worker (×32)
  participant K as Kafka
  participant W as Worker goroutines (×50×3)
  participant R as Redis

  loop every ~100ms per user (avg)
    U->>P: POST /rpc {method, params}
    P->>R: SET request_id → pending
    P->>KCh: push message
    P-->>U: {request_id, status:"accepted"} ~1-5ms
    Note over U: sleep rand(0, GAP)
  end

  KW->>KCh: drain message
  KW->>K: publish to topic
  KW->>R: SET request_id → queued

  K->>W: consume message
  W->>W: call upstream RPC
  W->>R: SET request_id → completed + result
```
- Runs until **Ctrl+C** or `DURATION` flag expires.

```
Each user goroutine:

  ┌─────────────────────────────────────────────────┐
  │  loop:                                          │
  │    POST /rpc  ──────────────────────────────►   │
  │               ◄──────────────────────────────   │
  │               {request_id, status:"accepted"}   │
  │    sleep rand(0, GAP)                           │
  └─────────────────────────────────────────────────┘
```

### Running the Load Test

```bash
# Default: 100 users, 0–200ms gap per user (~1,000 req/s), runs until Ctrl+C
task test:load

# Custom parameters
task test:load USERS=50 GAP=400ms           # ~250 req/s (light)
task test:load USERS=100 GAP=100ms          # ~2,000 req/s (stress)
task test:load USERS=100 GAP=200ms DURATION=5m  # fixed duration

# Override target (bypass ALB, hit single proxy directly)
PROXY_ADDR=http://172.19.0.5:8545 task test:load
```

The task auto-detects the ALB (`drpc-local-alb.elb.localhost.localstack.cloud`) if available, otherwise falls back to a single proxy container IP.

### QPS Capacity

Capacity is governed by the worker processing pipeline:

$$\text{max msg/s} = \frac{\text{worker tasks} \times \text{goroutines per task}}{\text{avg upstream latency}}$$

| Scenario | Workers | Goroutines | Avg Latency | Capacity |
|---|---|---|---|---|
| **Local mock (default)** | 3 | 50 | 150ms | **~1,000 msg/s** |
| Local mock (fast) | 3 | 50 | 30ms | ~5,000 msg/s |
| Production (real upstream) | 3 | 50 | 300ms | ~500 msg/s |
| Scale-out | 6 | 50 | 150ms | ~2,000 msg/s |

> **Kafka partitions = ceiling on worker tasks.** The topic has 3 partitions; adding a 4th worker container gains nothing. To scale beyond ~1,000 msg/s with 150ms latency, increase partitions and `worker_desired_count` together.

> **The numbers above reflect the current default configuration, not a design ceiling.** Throughput scales linearly with the number of worker tasks, goroutines per task, and Kafka partitions — all of which are runtime variables (`worker_desired_count`, `worker_goroutines`, partition count). The load test itself is equally unconstrained: increasing `USERS` or decreasing `GAP` can drive far higher ingest rates. The capacity formula above gives the relationship — tune instances and settings to match your target QPS.

**Default load test vs capacity:**

| Parameter | Value |
|---|---|
| Users | 100 |
| Gap | 0–200ms (avg 100ms) |
| Ingest rate | ~1,000 req/s |
| Worker capacity (150ms latency) | ~1,000 msg/s |
| Kafka lag (steady state) | ≈ 0 |

**Proxy ingest path latency** (POST `/rpc`, no upstream involved):

| Percentile | Observed |
|---|---|
| p50 | < 5ms |
| p95 | < 15ms |
| p99 | < 30ms |

The proxy itself is not the bottleneck. The semaphore (1,000 slots) and the `kafkaCh` buffer (2,000 slots per instance × 3 instances = 6,000 total) provide headroom for burst traffic well beyond the worker consumption rate, with Kafka acting as the durable overflow buffer.

![](doc/Screenshot_20260405_190602.png)

---

## 4. Demo

<video src="https://github.com/JasonYangShadow/drpc_proxy/blob/main/doc/2026-04-05%2019-01-19_compressed.mp4" controls width="100%"></video>
