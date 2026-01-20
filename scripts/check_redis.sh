#!/bin/bash
# check_redis.sh
# Check Redis queue and snapshot status
# Usage: ./scripts/check_redis.sh

set -e

echo "========================================"
echo "Redis Status Check"
echo "========================================"

# Run inside Docker context
docker compose exec -T redis redis-cli <<'EOF'
echo "=== Queue Status ==="
echo "Pending tasks:"
LLEN animetop:queue:crawl:pending

echo ""
echo "Processing tasks:"
LLEN animetop:queue:crawl:processing

echo ""
echo "Results:"
LLEN animetop:queue:crawl:results

echo ""
echo "=== Snapshot Keys ==="
echo "Current snapshots:"
KEYS animetop:snapshot:*:current

echo ""
echo "=== Status Keys ==="
echo "Item status hashes (sample 10):"
SCAN 0 MATCH animetop:status:* COUNT 10

echo ""
echo "=== Memory Stats ==="
INFO memory | grep -E "(used_memory_human|maxmemory_human)"
EOF

echo ""
echo "========================================"
