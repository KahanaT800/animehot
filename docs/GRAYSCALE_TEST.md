# Grayscale Testing Guide

This document describes how to perform grayscale testing with 1-2 IPs before full deployment.

## Prerequisites

- Docker and Docker Compose installed
- At least 2GB RAM available (t3.small compatible)
- `jq` and `curl` installed for verification scripts

## Quick Start

### Option 1: Full Stack (with Crawler)

```bash
# Start everything and import test IPs
make grayscale-start

# Verify data flow
make grayscale-verify

# Monitor logs
make docker-logs
```

### Option 2: Light Stack (without Crawler)

For t3.small or when running crawler separately:

```bash
# Start light stack
make grayscale-start-light

# Run crawler locally or on separate machine
make dev-crawler
```

## Test IPs

The grayscale test uses 2 IPs defined in `data/ips_grayscale.json`:

| Name | Category | Weight | Notes |
|------|----------|--------|-------|
| Hatsune Miku | vocaloid | 1.0 | High volume, stable market |
| Frieren | anime | 1.5 | Trending anime |

## Verification Steps

### 1. API Health Check

```bash
curl http://localhost:8080/health
# Expected: {"status":"ok"}
```

### 2. Check Imported IPs

```bash
curl http://localhost:8080/api/v1/ips | jq
```

### 3. Check System Status

```bash
curl http://localhost:8080/api/v1/system/status | jq
```

### 4. Check Scheduler

```bash
curl http://localhost:8080/api/v1/system/scheduler | jq
```

### 5. Manually Trigger Crawl

```bash
# Trigger specific IP
curl -X POST http://localhost:8080/api/v1/ips/1/trigger

# Trigger all IPs
make grayscale-trigger
```

### 6. Check Redis Queue

```bash
make grayscale-check-redis
```

### 7. Check MySQL Data

```bash
make grayscale-check-mysql
```

### 8. Full Verification

```bash
make grayscale-verify
```

## Expected Data Flow

```
1. Scheduler checks IP schedules every 10 seconds
   └─> If due, creates CrawlRequest in Redis queue

2. Crawler pulls CrawlRequest from queue
   └─> Fetches Mercari search results (on_sale + sold)
   └─> Pushes CrawlResponse to results queue

3. Pipeline consumes CrawlResponse
   └─> DiffEngine: saves snapshot, computes diff
   └─> StatusTracker: tracks status transitions
   └─> Aggregator: updates MySQL stats
   └─> Creates alerts if thresholds exceeded

4. API serves data from MySQL
```

## Monitoring

### View Logs

```bash
# All services
make docker-logs

# Analyzer only
make docker-logs-analyzer

# Crawler only
make docker-logs-crawler
```

### Key Log Messages

**Scheduler:**
```
level=info msg="Scheduling IP" ip_id=1 ip_name="Hatsune Miku" next_run=...
level=info msg="Pushed crawl tasks" ip_id=1 tasks=2
```

**Crawler:**
```
level=info msg="Processing crawl request" ip_id=1 keyword="Hatsune Miku" status=on_sale
level=info msg="Crawl completed" ip_id=1 items_found=120
```

**Pipeline:**
```
level=info msg="Processing crawl response" ip_id=1 items=120
level=info msg="Computed diff" inflow=5 outflow=3
level=info msg="Saved hourly stats" ip_id=1 liquidity_index=0.85
```

## Troubleshooting

### No data after waiting

1. Check if crawler is running:
   ```bash
   docker compose ps
   ```

2. Check Redis queue:
   ```bash
   make grayscale-check-redis
   ```

3. Check crawler logs:
   ```bash
   docker compose logs crawler --tail=100
   ```

4. Manually trigger:
   ```bash
   make grayscale-trigger
   ```

### Crawler errors

1. Check if Chromium is working:
   ```bash
   docker compose exec crawler chromium-browser --version
   ```

2. Check memory usage:
   ```bash
   docker stats
   ```

3. Reduce concurrency in `.env`:
   ```
   BROWSER_MAX_CONCURRENCY=1
   BROWSER_MAX_FETCH_COUNT=30
   ```

### Database connection errors

1. Check MySQL is healthy:
   ```bash
   docker compose exec mysql mysqladmin ping -u animetop -panimetop_pass
   ```

2. Check migrations ran:
   ```bash
   make db-shell
   SHOW TABLES;
   ```

## Memory Usage (t3.small 2GB)

Expected memory distribution:

| Service | Limit | Typical |
|---------|-------|---------|
| MySQL | 512MB | 300-400MB |
| Redis | 192MB | 50-100MB |
| Analyzer | 256MB | 100-150MB |
| Crawler | 768MB | 400-600MB |
| **Total** | **1.7GB** | **~1.2GB** |

For light mode (no crawler): ~700MB total

## Cleanup

```bash
# Stop services (keep data)
make grayscale-stop

# Remove everything including data
make grayscale-clean
```

## Success Criteria

Grayscale test is successful when:

- [ ] All services start without errors
- [ ] IPs are imported and visible via API
- [ ] Scheduler triggers crawls at expected intervals
- [ ] Crawler fetches data without errors
- [ ] Pipeline processes results and updates MySQL
- [ ] `ip_stats_hourly` has data for test IPs
- [ ] `item_snapshots` has item records
- [ ] Memory usage stays within limits
- [ ] No alerts for test IPs (or expected alerts only)

## Next Steps

After successful grayscale testing:

1. Import production IP list:
   ```bash
   make docker-import-run FILE=data/ips_production.json
   ```

2. Adjust scheduler intervals based on traffic

3. Enable monitoring (Prometheus/Grafana)

4. Set up alerts for critical thresholds
