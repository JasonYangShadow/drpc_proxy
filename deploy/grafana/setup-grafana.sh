#!/usr/bin/env bash
set -euo pipefail

GRAFANA_IP=$(docker exec grafana hostname -i 2>/dev/null | awk '{print $1}')
echo "Waiting for Grafana at $GRAFANA_IP:3000..."
for i in $(seq 1 30); do
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://$GRAFANA_IP:3000/api/health" 2>/dev/null || echo "000")
  [ "$STATUS" = "200" ] && break
  sleep 2
done

echo "Provisioning Grafana datasource..."
curl -sf -X POST "http://$GRAFANA_IP:3000/api/datasources" \
  -H "Content-Type: application/json" \
  -d '{"name":"Prometheus","type":"prometheus","access":"proxy","url":"http://prometheus:9090","isDefault":true,"uid":"prometheus"}' \
  > /dev/null 2>&1 || true

echo "Provisioning Grafana dashboard..."
DASHBOARD=$(cat "$(dirname "$0")/provisioning/dashboards/drpc.json")
curl -sf -X POST "http://$GRAFANA_IP:3000/api/dashboards/db" \
  -H "Content-Type: application/json" \
  -d "{\"dashboard\":$DASHBOARD,\"folderId\":0,\"overwrite\":true}" \
  > /dev/null

echo "Grafana ready → http://localhost:3000/d/drpc-proxy-overview/drpc-proxy"
