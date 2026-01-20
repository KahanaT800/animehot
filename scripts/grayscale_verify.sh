#!/bin/bash
# grayscale_verify.sh
# Verification script for grayscale testing
# Usage: ./scripts/grayscale_verify.sh [api_host]

set -e

API_HOST="${1:-localhost:8080}"
API_BASE="http://${API_HOST}/api/v1"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "========================================"
echo "Animetop Grayscale Test Verification"
echo "========================================"
echo "API Host: ${API_HOST}"
echo ""

# 1. Health Check
echo -e "${YELLOW}[1/7] Health Check${NC}"
HEALTH=$(curl -s "http://${API_HOST}/health")
if echo "$HEALTH" | grep -q "ok"; then
    echo -e "${GREEN}  OK: API server is healthy${NC}"
else
    echo -e "${RED}  FAIL: API server not responding${NC}"
    echo "  Response: $HEALTH"
    exit 1
fi

# 2. List IPs
echo -e "\n${YELLOW}[2/7] Checking imported IPs${NC}"
IPS_RESPONSE=$(curl -s "${API_BASE}/ips")
IP_COUNT=$(echo "$IPS_RESPONSE" | jq -r '.data.total // 0')
echo "  Total IPs: $IP_COUNT"

if [ "$IP_COUNT" -eq 0 ]; then
    echo -e "${RED}  FAIL: No IPs found. Please import test data first.${NC}"
    echo "  Run: make docker-import-run FILE=data/ips_grayscale.json"
    exit 1
fi

echo "  IP List:"
echo "$IPS_RESPONSE" | jq -r '.data.items[] | "    - [\(.id)] \(.name) (weight: \(.weight), status: \(.status))"'

# 3. Check System Status
echo -e "\n${YELLOW}[3/7] System Status${NC}"
SYSTEM_STATUS=$(curl -s "${API_BASE}/system/status")
echo "  Scheduler Active IPs: $(echo "$SYSTEM_STATUS" | jq -r '.data.scheduler.active_ips // "N/A"')"
echo "  Pipeline Stats:"
echo "    - Processed: $(echo "$SYSTEM_STATUS" | jq -r '.data.pipeline.processed // 0')"
echo "    - Errors: $(echo "$SYSTEM_STATUS" | jq -r '.data.pipeline.errors // 0')"

# 4. Check Scheduler Status
echo -e "\n${YELLOW}[4/7] Scheduler Status${NC}"
SCHEDULER=$(curl -s "${API_BASE}/system/scheduler")
echo "$SCHEDULER" | jq -r '.data.schedules[]? | "    - [\(.ip_id)] \(.ip_name): next=\(.next_run) interval=\(.interval)"' 2>/dev/null || echo "  No schedules found yet"

# 5. Check IP Stats (for first IP)
echo -e "\n${YELLOW}[5/7] IP Statistics${NC}"
FIRST_IP_ID=$(echo "$IPS_RESPONSE" | jq -r '.data.items[0].id // empty')
if [ -n "$FIRST_IP_ID" ]; then
    echo "  Checking IP ID: $FIRST_IP_ID"

    # Liquidity
    LIQUIDITY=$(curl -s "${API_BASE}/ips/${FIRST_IP_ID}/liquidity")
    echo "  Liquidity Data:"
    echo "    - On Sale Current: $(echo "$LIQUIDITY" | jq -r '.data.on_sale.current // 0')"
    echo "    - On Sale Inflow: $(echo "$LIQUIDITY" | jq -r '.data.on_sale.inflow // 0')"
    echo "    - On Sale Outflow: $(echo "$LIQUIDITY" | jq -r '.data.on_sale.outflow // 0')"
    echo "    - Sold Inflow: $(echo "$LIQUIDITY" | jq -r '.data.sold.inflow // 0')"
    echo "    - Liquidity Index: $(echo "$LIQUIDITY" | jq -r '.data.liquidity_index // "N/A"')"

    # Hourly Stats
    STATS=$(curl -s "${API_BASE}/ips/${FIRST_IP_ID}/stats/hourly?limit=5")
    STATS_COUNT=$(echo "$STATS" | jq -r '.data | length // 0')
    echo "  Hourly Stats Records: $STATS_COUNT"

    # Items
    ITEMS=$(curl -s "${API_BASE}/ips/${FIRST_IP_ID}/items?limit=5")
    ITEMS_COUNT=$(echo "$ITEMS" | jq -r '.data.total // 0')
    echo "  Item Snapshots: $ITEMS_COUNT"
else
    echo "  No IP found to check"
fi

# 6. Check Alerts
echo -e "\n${YELLOW}[6/7] Alerts${NC}"
ALERTS=$(curl -s "${API_BASE}/alerts")
ALERT_COUNT=$(echo "$ALERTS" | jq -r '.data | length // 0')
echo "  Pending Alerts: $ALERT_COUNT"
if [ "$ALERT_COUNT" -gt 0 ]; then
    echo "$ALERTS" | jq -r '.data[]? | "    - [\(.severity)] \(.alert_type): \(.message)"'
fi

# 7. Summary
echo -e "\n${YELLOW}[7/7] Summary${NC}"
echo "========================================"

if [ "$STATS_COUNT" -gt 0 ] || [ "$ITEMS_COUNT" -gt 0 ]; then
    echo -e "${GREEN}Data flow is working!${NC}"
    echo "  - Crawler is fetching data"
    echo "  - Pipeline is processing results"
    echo "  - Stats are being aggregated"
else
    echo -e "${YELLOW}Waiting for data...${NC}"
    echo "  - Check if crawler is running: docker compose logs -f crawler"
    echo "  - Check Redis queue: docker compose exec redis redis-cli LLEN animetop:queue:crawl:pending"
    echo "  - Manual trigger: curl -X POST ${API_BASE}/ips/<id>/trigger"
fi

echo ""
echo "Next steps:"
echo "  1. Monitor logs: make docker-logs"
echo "  2. Wait for scheduler to trigger crawls"
echo "  3. Re-run this script to check progress"
echo "========================================"
