#!/bin/bash
# check_mysql.sh
# Check MySQL data status
# Usage: ./scripts/check_mysql.sh

set -e

echo "========================================"
echo "MySQL Data Status Check"
echo "========================================"

# Run inside Docker context
docker compose exec -T mysql mysql -u animetop -panimetop_pass animetop <<'EOF'
SELECT '=== IP Metadata ===' AS '';
SELECT id, name, category, weight, status,
       DATE_FORMAT(last_crawled_at, '%Y-%m-%d %H:%i') as last_crawled
FROM ip_metadata;

SELECT '' AS '';
SELECT '=== Hourly Stats (Last 24h) ===' AS '';
SELECT
    s.ip_id,
    m.name,
    DATE_FORMAT(s.hour_bucket, '%m-%d %H:00') as hour,
    s.inflow,
    s.outflow,
    ROUND(s.liquidity_index, 2) as liquidity
FROM ip_stats_hourly s
JOIN ip_metadata m ON s.ip_id = m.id
WHERE s.hour_bucket >= NOW() - INTERVAL 24 HOUR
ORDER BY s.hour_bucket DESC
LIMIT 20;

SELECT '' AS '';
SELECT '=== Item Snapshots Summary ===' AS '';
SELECT
    m.name,
    COUNT(*) as total_items,
    SUM(CASE WHEN i.status = 'on_sale' THEN 1 ELSE 0 END) as on_sale,
    SUM(CASE WHEN i.status = 'sold' THEN 1 ELSE 0 END) as sold,
    ROUND(AVG(i.price), 0) as avg_price
FROM item_snapshots i
JOIN ip_metadata m ON i.ip_id = m.id
GROUP BY i.ip_id, m.name;

SELECT '' AS '';
SELECT '=== Recent Alerts ===' AS '';
SELECT
    id,
    (SELECT name FROM ip_metadata WHERE id = ip_id) as ip_name,
    alert_type,
    severity,
    LEFT(message, 50) as message,
    acknowledged
FROM ip_alerts
ORDER BY created_at DESC
LIMIT 10;

SELECT '' AS '';
SELECT '=== Table Row Counts ===' AS '';
SELECT
    (SELECT COUNT(*) FROM ip_metadata) as ip_count,
    (SELECT COUNT(*) FROM ip_stats_hourly) as stats_count,
    (SELECT COUNT(*) FROM item_snapshots) as items_count,
    (SELECT COUNT(*) FROM ip_alerts) as alerts_count;
EOF

echo ""
echo "========================================"
