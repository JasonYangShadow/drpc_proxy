#!/usr/bin/env bash
# deploy/localstack/bootstrap.sh
#
# Registers all AWS resources (ECS cluster, task definitions, services, ECR
# repos, log groups, IAM roles, secrets) against the running LocalStack
# instance at http://localhost:4566.
#
# Usage:
#   ./deploy/localstack/bootstrap.sh [--endpoint-url <url>]
#
# Prerequisites:
#   - AWS CLI v2  (brew install awscli  OR  pip install awscli)
#   - LocalStack running:  docker compose up -d localstack
#   - Images built:        docker compose build
#
# Note: LocalStack Community edition registers resources but does not schedule
#       containers. Use `docker compose up proxy worker` for actual execution.
#       LocalStack Pro (LOCALSTACK_AUTH_TOKEN set) runs tasks via Docker.

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
ENDPOINT="${LOCALSTACK_ENDPOINT:-http://localhost:4566}"
REGION="${AWS_DEFAULT_REGION:-us-east-1}"
ACCOUNT="000000000000"   # LocalStack default account ID
CLUSTER="drpc-cluster"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# LocalStack credentials (any non-empty strings work)
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_DEFAULT_REGION="${REGION}"

AWSCLI="aws --endpoint-url=${ENDPOINT}"

log()  { echo "[bootstrap] $*"; }
ok()   { echo "[bootstrap] ✓ $*"; }
fail() { echo "[bootstrap] ✗ $*" >&2; exit 1; }

# ── Wait for LocalStack ────────────────────────────────────────────────────────
log "Waiting for LocalStack at ${ENDPOINT} ..."
for i in $(seq 1 30); do
  if curl -sf "${ENDPOINT}/_localstack/health" | grep -q '"running"'; then
    ok "LocalStack is up"
    break
  fi
  if [[ $i -eq 30 ]]; then
    fail "LocalStack did not become healthy after 30 tries. Is 'docker compose up -d localstack' running?"
  fi
  sleep 2
done

# ── IAM roles ─────────────────────────────────────────────────────────────────
log "Creating IAM roles ..."

TRUST_POLICY='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

for ROLE in ecsTaskExecutionRole ecsTaskRole; do
  if ! $AWSCLI iam get-role --role-name "${ROLE}" &>/dev/null; then
    $AWSCLI iam create-role \
      --role-name "${ROLE}" \
      --assume-role-policy-document "${TRUST_POLICY}" \
      --output text --query 'Role.RoleName' | xargs -I{} echo "  created role: {}"
  else
    echo "  role already exists: ${ROLE}"
  fi
done
ok "IAM roles ready"

# ── Secrets Manager ───────────────────────────────────────────────────────────
log "Creating secrets ..."

KAFKA_ADDR="${KAFKA_ADDR:-kafka:9092}"
REDIS_ADDR="${REDIS_ADDR:-redis:6379}"

for SECRET_NAME in drpc/kafka-addr drpc/redis-addr; do
  VALUE="${KAFKA_ADDR}"
  [[ "${SECRET_NAME}" == "drpc/redis-addr" ]] && VALUE="${REDIS_ADDR}"

  if ! $AWSCLI secretsmanager describe-secret --secret-id "${SECRET_NAME}" &>/dev/null; then
    $AWSCLI secretsmanager create-secret \
      --name "${SECRET_NAME}" \
      --secret-string "${VALUE}" \
      --output text --query 'Name' | xargs -I{} echo "  created secret: {}"
  else
    $AWSCLI secretsmanager update-secret \
      --secret-id "${SECRET_NAME}" \
      --secret-string "${VALUE}" \
      --output text --query 'Name' | xargs -I{} echo "  updated secret: {}"
  fi
done
ok "Secrets ready"

# ── CloudWatch Log Groups ──────────────────────────────────────────────────────
log "Creating CloudWatch log groups ..."

for LOG_GROUP in /ecs/drpc-proxy /ecs/drpc-worker; do
  if ! $AWSCLI logs describe-log-groups --log-group-name-prefix "${LOG_GROUP}" \
       --query 'logGroups[0].logGroupName' --output text 2>/dev/null | grep -q "${LOG_GROUP}"; then
    $AWSCLI logs create-log-group --log-group-name "${LOG_GROUP}"
    $AWSCLI logs put-retention-policy --log-group-name "${LOG_GROUP}" --retention-in-days 7
    echo "  created log group: ${LOG_GROUP}"
  else
    echo "  log group already exists: ${LOG_GROUP}"
  fi
done
ok "Log groups ready"

# ── ECR Repositories ──────────────────────────────────────────────────────────
log "Creating ECR repositories ..."

ECR_ENDPOINT="${ACCOUNT}.dkr.ecr.${REGION}.localhost.localstack.cloud:4566"

for REPO in drpc-proxy drpc-worker; do
  if ! $AWSCLI ecr describe-repositories --repository-names "${REPO}" &>/dev/null; then
    $AWSCLI ecr create-repository --repository-name "${REPO}" \
      --output text --query 'repository.repositoryName' | xargs -I{} echo "  created repo: {}"
  else
    echo "  ECR repo already exists: ${REPO}"
  fi
done
ok "ECR repositories ready"

# ── Push images to LocalStack ECR ─────────────────────────────────────────────
log "Tagging and pushing images to LocalStack ECR ..."

# Authenticate Docker to LocalStack ECR (no-op on Community but required on Pro)
$AWSCLI ecr get-login-password | \
  docker login --username AWS --password-stdin "${ECR_ENDPOINT}" 2>/dev/null || true

for REPO in drpc-proxy drpc-worker; do
  LOCAL_TAG="${REPO}:local"
  REMOTE_TAG="${ECR_ENDPOINT}/${REPO}:latest"

  if docker image inspect "${LOCAL_TAG}" &>/dev/null; then
    docker tag "${LOCAL_TAG}" "${REMOTE_TAG}"
    docker push "${REMOTE_TAG}" && echo "  pushed: ${REMOTE_TAG}" || echo "  push skipped (Community ECR is metadata-only)"
  else
    echo "  image '${LOCAL_TAG}' not found — run 'docker compose build' first"
  fi
done
ok "Images pushed"

# ── ECS Cluster ───────────────────────────────────────────────────────────────
log "Creating ECS cluster '${CLUSTER}' ..."

if ! $AWSCLI ecs describe-clusters --clusters "${CLUSTER}" \
     --query 'clusters[0].status' --output text 2>/dev/null | grep -q "ACTIVE"; then
  $AWSCLI ecs create-cluster \
    --cluster-name "${CLUSTER}" \
    --capacity-providers FARGATE FARGATE_SPOT \
    --output text --query 'cluster.clusterName' | xargs -I{} echo "  created cluster: {}"
else
  echo "  cluster already active: ${CLUSTER}"
fi
ok "ECS cluster ready"

# ── Task Definitions ──────────────────────────────────────────────────────────
log "Registering ECS task definitions ..."

for TASK in task-proxy task-worker; do
  TASK_FILE="${DEPLOY_DIR}/ecs/${TASK}.json"
  FAMILY=$(python3 -c "import json,sys; print(json.load(open('${TASK_FILE}'))['family'])")

  REV=$($AWSCLI ecs register-task-definition \
    --cli-input-json "file://${TASK_FILE}" \
    --output text --query 'taskDefinition.revision')
  echo "  registered ${FAMILY}:${REV}"
done
ok "Task definitions registered"

# ── ECS Services ──────────────────────────────────────────────────────────────
# Uses a placeholder subnet/security-group — LocalStack doesn't validate them.
log "Creating ECS services ..."

FAKE_SUBNET="subnet-00000000000000001"
FAKE_SG="sg-00000000000000001"

declare -A SERVICE_TASK
SERVICE_TASK["drpc-proxy-service"]="drpc-proxy"
SERVICE_TASK["drpc-worker-service"]="drpc-worker"

for SERVICE in drpc-proxy-service drpc-worker-service; do
  FAMILY="${SERVICE_TASK[$SERVICE]}"

  if ! $AWSCLI ecs describe-services \
       --cluster "${CLUSTER}" --services "${SERVICE}" \
       --query 'services[0].status' --output text 2>/dev/null | grep -q "ACTIVE"; then
    $AWSCLI ecs create-service \
      --cluster "${CLUSTER}" \
      --service-name "${SERVICE}" \
      --task-definition "${FAMILY}" \
      --desired-count 1 \
      --launch-type FARGATE \
      --network-configuration "awsvpcConfiguration={subnets=[${FAKE_SUBNET}],securityGroups=[${FAKE_SG}],assignPublicIp=ENABLED}" \
      --output text --query 'service.serviceName' | xargs -I{} echo "  created service: {}"
  else
    $AWSCLI ecs update-service \
      --cluster "${CLUSTER}" \
      --service "${SERVICE}" \
      --task-definition "${FAMILY}" \
      --desired-count 1 \
      --output text --query 'service.serviceName' | xargs -I{} echo "  updated service: {}"
  fi
done
ok "ECS services ready"

# ── Summary ───────────────────────────────────────────────────────────────────
cat <<EOF

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
LocalStack bootstrap complete
  Endpoint : ${ENDPOINT}
  Region   : ${REGION}
  Cluster  : ${CLUSTER}

Verify with:
  aws --endpoint-url=${ENDPOINT} ecs list-task-definitions
  aws --endpoint-url=${ENDPOINT} ecs list-services --cluster ${CLUSTER}
  aws --endpoint-url=${ENDPOINT} secretsmanager list-secrets

To run containers locally (docker-compose, not ECS scheduling):
  docker compose up proxy worker
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
EOF
