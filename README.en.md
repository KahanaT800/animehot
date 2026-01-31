# Anime Hot
**[English](README.en.md)** | **[日本語](README.ja.md)** | **[中文](README.md)**

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Python Version](https://img.shields.io/badge/Python-3.11+-3776AB?style=flat&logo=python)](https://python.org/)
[![K3s](https://img.shields.io/badge/K3s-Lightweight%20K8s-FFC61C?style=flat&logo=k3s)](https://k3s.io/)
[![Redis](https://img.shields.io/badge/Redis-7.x-DC382D?style=flat&logo=redis)](https://redis.io/)
[![Grafana](https://img.shields.io/badge/Grafana-Cloud-F46800?style=flat&logo=grafana)](https://grafana.com/)
[![CI](https://github.com/KahanaT800/animehot/actions/workflows/ci.yml/badge.svg)](https://github.com/KahanaT800/animehot/actions/workflows/ci.yml)
[![Deploy](https://github.com/KahanaT800/animehot/actions/workflows/deploy.yml/badge.svg)](https://github.com/KahanaT800/animehot/actions/workflows/deploy.yml)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Website](https://img.shields.io/website?url=https%3A%2F%2Fanime-hot.com&label=anime-hot.com)](https://anime-hot.com)

# [🔥 Check out the hottest anime rankings now →](https://anime-hot.com)

Real-time anime IP liquidity tracker for Japanese second-hand market.

## Overview

Anime Hot analyzes the flow of anime merchandise on [Mercari](https://jp.mercari.com/) (Japan's largest second-hand marketplace) to calculate "liquidity indices" for anime IPs. By tracking hourly inflow (new listings) and outflow (sold items), it identifies trending IPs and detects fan exodus patterns.

### Key Metrics

| Metric | Formula | Meaning |
|--------|---------|---------|
| **Inflow** | New listings per hour | Supply entering the market |
| **Outflow** | Items sold per hour | Demand / actual sales |
| **Liquidity Index** | Outflow / Inflow | Market velocity |
| **Hot Score** | (Out+1)/(In+1) × log(Out+1) | Weighted popularity score |

### Use Cases

- **Trend Detection**: Identify IPs gaining or losing popularity
- **Market Analysis**: Compare supply/demand across different anime franchises
- **Investment Signals**: Spot undervalued or overheated collectibles

## Screenshots

<p align="center">
  <img src="docs/screenshots/dashboard.png" alt="Dashboard" width="800">
  <br>
  <em>Real-time leaderboard showing hot anime IPs</em>
</p>

<p align="center">
  <img src="docs/screenshots/ip-detail.png" alt="IP Detail" width="800">
  <br>
  <em>Detailed liquidity analysis for individual IP</em>
</p>

<p align="center">
  <img src="docs/screenshots/grafana.png" alt="Grafana Monitoring" width="800">
  <br>
  <em>Grafana Cloud monitoring dashboard</em>
</p>

## Architecture

### System Overview

```mermaid
graph TB
    CLIENT[Client]

    subgraph EC2[AWS EC2 - Main Node / K3s Server]
        NGINX[Nginx :80/:443]
        ANALYZER[Analyzer :8080]
        MYSQL[(MySQL)]
        REDIS[(Redis)]
        KEDA[KEDA]
        SCALER[Spot ASG Scaler]
    end

    subgraph SPOT[AWS Spot Node Pool - K3s Agent]
        CRAWLER1[py-crawler Pod]
        CRAWLER2[py-crawler Pod]
        ALLOY[Alloy DaemonSet]
    end

    MERCARI[Mercari]
    GRAFANA[Grafana Cloud]
    ASG[EC2 Auto Scaling Group]

    CLIENT --> NGINX
    NGINX --> ANALYZER
    ANALYZER <--> MYSQL
    ANALYZER <--> REDIS

    KEDA -->|Queue Depth| SCALER
    SCALER -->|Adjust Capacity| ASG
    ASG -->|Launch/Terminate| SPOT

    CRAWLER1 & CRAWLER2 -.->|Tailscale VPN| REDIS
    CRAWLER1 & CRAWLER2 --> MERCARI

    ALLOY --> GRAFANA
```

### Core Design

- **K3s Cluster**: EC2 main node as Server, Spot instances as Agent nodes
- **KEDA Auto-scaling**: Automatically adjusts py-crawler replicas based on Redis queue depth
- **Spot ASG Scaler**: Launches Spot nodes when Pods are pending, auto-terminates on idle
- **Tailscale VPN**: Spot nodes connect to main node's Redis via Tailscale
- **Grafana Alloy**: Deployed as DaemonSet, auto-collects metrics and logs from all nodes

### Task Flow

```mermaid
sequenceDiagram
    participant ZSET as Redis ZSET
    participant SCH as Scheduler
    participant RQ as Redis Queue
    participant CRW as Crawler (Any)
    participant SM as State Machine
    participant PIP as Pipeline
    participant DB as MySQL

    SCH->>ZSET: Query next schedule time
    SCH->>SCH: Precise sleep until that time
    SCH->>ZSET: Get due IPs
    SCH->>RQ: Push crawl tasks

    loop Worker Loop
        CRW->>RQ: Pull task (BRPOP)
        CRW->>CRW: HTTP auth request
        CRW->>CRW: Crawl 5 pages on_sale
        CRW->>CRW: Crawl 5 pages sold
        CRW->>RQ: Push result
    end

    PIP->>RQ: Pull result
    PIP->>SM: Process items batch
    SM->>SM: Detect transitions<br/>(new_listing, sold, price_change)
    SM-->>PIP: Return transitions
    PIP->>PIP: Calculate metrics<br/>(inflow, outflow, liquidity)
    PIP->>DB: Write hourly stats
    PIP->>ZSET: Update next schedule (closed-loop)
    PIP->>RQ: Invalidate cache
```

## Tech Stack

- **Backend**: Go 1.24+ (Gin + GORM)
- **Crawler**: Python 3.11+ (HTTP auth + Playwright fallback)
- **Message Format**: Protocol Buffers (protojson)
- **Database**: MySQL 8.0 + Redis 7.x
- **Monitoring**: Prometheus + Grafana Cloud + Alloy

## Quick Start

### Prerequisites

- Docker & Docker Compose
- Go 1.24+ (for local development)

### Local Development

```bash
# Clone the repository
git clone https://github.com/KahanaT800/animehot
cd animehot

# Copy environment file
cp .env.example .env

# Start infrastructure (MySQL + Redis)
make dev-deps

# Run analyzer (in terminal 1)
make dev-analyzer

# Run crawler (in terminal 2)
make dev-crawler

# Import test IPs
make api-import-run FILE=data/ips.json
```

### Docker Deployment

```bash
# Full stack (MySQL + Redis + Analyzer + Crawler)
make docker-up

# Light mode (no crawler, for testing)
make docker-light-up

# With monitoring (Grafana Cloud)
make docker-up-monitoring

# View logs
make docker-logs
```

## Production Deployment

### EC2 Setup

```bash
# 1. Install Docker
sudo yum update -y
sudo yum install -y docker git
sudo systemctl start docker && sudo systemctl enable docker
sudo usermod -aG docker ec2-user

# 2. Install Docker Compose
sudo curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64" \
  -o /usr/local/bin/docker-compose
sudo chmod +x /usr/local/bin/docker-compose

# 3. Install Tailscale (for distributed crawlers)
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up

# 4. Clone and configure
git clone https://github.com/lyc0603/animetop.git
cd animetop
cp .env.example .env
# Edit .env with production values

# 5. Initialize SSL and start services
export DOMAIN_NAME=your-domain.com
export LETSENCRYPT_EMAIL=admin@your-domain.com
./deploy/certbot/init-letsencrypt.sh
```

### Security Checklist

- [ ] MySQL port (3306) not exposed externally
- [ ] Redis accessed only via Tailscale VPN
- [ ] Admin API protected with `ADMIN_API_KEY`
- [ ] HTTPS enforced with HSTS
- [ ] `/metrics` endpoint blocked from external access

## K8s/Spot Distributed Crawler

Use AWS Spot instances to scale crawler capacity on-demand. Cost is 10-30% of on-demand instances.

### Architecture Overview

```
EC2 Main Node (K3s Server)          Spot Node Pool (K3s Agent)
┌─────────────────────┐          ┌─────────────────────┐
│  Analyzer           │          │  py-crawler Pod     │
│  MySQL / Redis      │◄─────────│  py-crawler Pod     │
│  KEDA               │ Tailscale│  Alloy DaemonSet    │
│  Spot ASG Scaler    │          └─────────────────────┘
└─────────────────────┘                    ▲
         │                                 │
         ▼                                 │
   ┌───────────┐     Adjust       ┌───────────────┐
   │Queue Depth│ ────────────────▶│   EC2 ASG     │
   └───────────┘                  └───────────────┘
```

### Auto-scaling Logic

| Trigger | Action |
|---------|--------|
| Queue depth > 0 | KEDA creates py-crawler Pods |
| Pod pending (no nodes) | Scaler launches Spot instances |
| Node idle 15min | Scaler terminates Spot instances |
| Spot interruption notice | Graceful Pod shutdown, auto node cleanup |

### K3s Cluster Initialization

```bash
# EC2 Main Node - Install K3s Server
curl -sfL https://get.k3s.io | sh -s - server \
  --tls-san $(tailscale ip -4) \
  --node-external-ip $(tailscale ip -4)

# Get join token
cat /var/lib/rancher/k3s/server/node-token

# Deploy K8s resources
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/secrets.yaml  # Fill in credentials first
kubectl apply -f k8s/py-crawler.yaml
kubectl apply -f k8s/keda-scaledobject.yaml
kubectl apply -f k8s/spot-asg-scaler.yaml
kubectl apply -f k8s/alloy-configmap.yaml
kubectl apply -f k8s/alloy-daemonset.yaml
```

### Key K8s Resources

| File | Description |
|------|-------------|
| `k8s/py-crawler.yaml` | py-crawler Deployment |
| `k8s/keda-scaledobject.yaml` | KEDA auto-scaling rules |
| `k8s/spot-asg-scaler.yaml` | Spot node management CronJob |
| `k8s/alloy-*.yaml` | Grafana Alloy monitoring configs |
| `k8s/secrets.yaml.template` | Credentials template |

## Monitoring

### Grafana Cloud Setup

1. Create a [Grafana Cloud](https://grafana.com/products/cloud/) account
2. Get Prometheus remote write credentials
3. Get Loki credentials (for logs)
4. Configure in `.env`:

```bash
GRAFANA_CLOUD_PROM_REMOTE_WRITE_URL=https://prometheus-xxx.grafana.net/api/prom/push
GRAFANA_CLOUD_PROM_USERNAME=your_username
GRAFANA_CLOUD_PROM_API_KEY=glc_xxx

GRAFANA_CLOUD_LOKI_URL=https://logs-xxx.grafana.net/loki/api/v1/push
GRAFANA_CLOUD_LOKI_USERNAME=your_username
GRAFANA_CLOUD_LOKI_API_KEY=glc_xxx
```

5. Start with monitoring profile:

```bash
docker compose -f docker-compose.prod.yml --profile monitoring up -d
```

### Dashboard Import

Import the business dashboard from `deploy/grafana/dashboards/animehot-business.json`:

| Section | Panels |
|---------|--------|
| Overview | Service Status, Active Tasks, Queue Depth |
| Spot Py-Crawler | Crawler Count, Task Progress, Latency, Auth Mode |
| Task Queue | Throughput, Queue Status |
| Redis Queues | DLQ, Schedule IPs, Task/Result Queue |

### Key Metrics

| Metric | Description |
|--------|-------------|
| `up{job="animetop-analyzer"}` | Analyzer health |
| `up{app="py-crawler", cluster="animehot-k3s"}` | Spot crawler health |
| `mercari_crawler_tasks_in_progress{cluster="animehot-k3s"}` | Spot tasks in progress |
| `mercari_crawler_api_request_duration_seconds` | API request latency |
| `mercari_crawler_auth_mode` | Auth mode (0=HTTP, 1=Browser) |
| `animetop_scheduler_tasks_pending_in_queue` | Queue depth |

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/api/v1/ips` | List all tracked IPs |
| GET | `/api/v1/ips/:id` | Get IP details |
| GET | `/api/v1/ips/:id/liquidity` | Get liquidity data |
| GET | `/api/v1/ips/:id/stats/hourly` | Get hourly statistics |
| GET | `/api/v1/ips/:id/items` | Get item listings |
| GET | `/api/v1/leaderboard` | Get rankings |
| GET | `/api/v1/system/status` | System status |
| POST | `/api/v1/admin/import` | Import IPs (requires API key) |

### Leaderboard API

```bash
# Get top 10 hot IPs in the last 24 hours
curl "http://localhost:8080/api/v1/leaderboard?type=hot&hours=24&limit=10"
```

Parameters:
- `type`: `hot` | `inflow` | `outflow`
- `hours`: 1-168 (time window)
- `limit`: 1-100

Response:
```json
{
  "code": 0,
  "data": {
    "type": "hot",
    "hours": 24,
    "time_range": {
      "start": "2026-01-17T17:00:00+09:00",
      "end": "2026-01-18T17:00:00+09:00"
    },
    "items": [
      {
        "rank": 1,
        "ip_id": 11,
        "ip_name": "Demon Slayer",
        "inflow": 355,
        "outflow": 28,
        "score": 0.2634
      }
    ]
  }
}
```

### Admin API

```bash
# Import IPs (requires X-API-Key header in production)
curl -X POST http://localhost:8080/api/v1/admin/import \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -d @data/ips.json
```

## Configuration

### Key Environment Variables

```bash
# Domain (for SSL)
DOMAIN_NAME=anime-hot.com
LETSENCRYPT_EMAIL=admin@anime-hot.com

# Security
ADMIN_API_KEY=your_secure_api_key

# Database
MYSQL_PASSWORD=your_secure_password

# Scheduler (ZSET persistence + precise sleep)
SCHEDULER_BASE_INTERVAL=2h      # Base crawl interval
SCHEDULER_MIN_INTERVAL=1h       # Min interval (hot IPs)
SCHEDULER_MAX_INTERVAL=2h       # Max interval

# Crawler
BROWSER_MAX_CONCURRENCY=2       # Concurrent browser tabs
SCHEDULER_PAGES_ON_SALE=5       # Pages to crawl (on sale)
SCHEDULER_PAGES_SOLD=5          # Pages to crawl (sold)
```

### Dynamic Interval Adjustment

The scheduler automatically adjusts crawl frequency based on activity (closed-loop update to Redis ZSET):

| Condition | Action |
|-----------|--------|
| inflow > 100×pages OR outflow > 100×pages | Speed up (-15min) |
| inflow < 50×pages AND outflow < 3×pages | Slow down (+15min) |
| Otherwise | Regress to 2h |

With default 5+5 pages:
- **Speed up**: inflow > 500 OR outflow > 500
- **Slow down**: inflow < 250 AND outflow < 15

## Make Commands

```bash
# Build
make build              # Build all Go binaries
make test               # Run tests
make lint               # Run linter

# Docker - Full Stack
make docker-up          # Start all services
make docker-down        # Stop all services
make docker-logs        # View logs

# Docker - Light (no crawler)
make docker-light-up    # Start MySQL + Redis + Analyzer
make docker-light-down  # Stop light services

# Docker - With Monitoring
make docker-up-monitoring    # Start all + Grafana Alloy
make docker-down-monitoring  # Stop all + monitoring

# Development
make dev-deps           # Start MySQL & Redis only
make dev-analyzer       # Run analyzer locally
make dev-crawler        # Run crawler locally

# Data Import
make api-import-run FILE=data/ips.json

# Grayscale Testing
make grayscale-start    # Full stack + test IPs
make grayscale-verify   # Verify data flow
make grayscale-clean    # Clean up
```

## Project Structure

```
animetop/
├── cmd/
│   ├── analyzer/          # API + Scheduler + Pipeline
│   ├── crawler/           # Headless browser worker
│   └── import/            # IP data import tool
├── internal/
│   ├── analyzer/          # Core analysis logic
│   │   ├── pipeline.go    # Result processing
│   │   ├── state_machine.go  # Item state tracking
│   │   └── cache.go       # Cache management
│   ├── api/               # HTTP handlers
│   ├── config/            # Configuration
│   ├── crawler/           # Browser automation (go-rod)
│   ├── model/             # Database models (GORM)
│   ├── pkg/               # Shared utilities
│   │   ├── metrics/       # Prometheus metrics
│   │   ├── ratelimit/     # Rate limiting
│   │   └── redisqueue/    # Reliable queue
│   └── scheduler/         # IP scheduling
├── k8s/                   # Kubernetes manifests
│   ├── py-crawler.yaml    # py-crawler Deployment
│   ├── keda-scaledobject.yaml  # KEDA auto-scaling
│   ├── spot-asg-scaler.yaml    # Spot node management
│   └── alloy-*.yaml       # Grafana Alloy DaemonSet
├── infra/aws/             # AWS infrastructure
│   └── user-data-spot.sh  # Spot instance bootstrap
├── deploy/
│   ├── nginx/             # Nginx configuration
│   ├── certbot/           # SSL initialization
│   ├── alloy/             # Grafana Alloy configs
│   └── grafana/           # Dashboard JSON
├── proto/                 # Protocol Buffers
├── migrations/            # Database migrations
├── data/                  # Test data (IP JSON)
├── docker-compose.yml           # Development
├── docker-compose.prod.yml      # Production (EC2)
└── docker-compose.crawler.yml   # Local crawler node
```

## Database Schema

### ip_metadata
Stores IP (Intellectual Property) information.

| Column | Type | Description |
|--------|------|-------------|
| id | INT | Primary key |
| name | VARCHAR | Japanese name |
| name_en | VARCHAR | English name |
| category | VARCHAR | Category (anime, game, etc.) |
| weight | FLOAT | Scheduling priority |

### ip_stats_hourly
Hourly statistics per IP.

| Column | Type | Description |
|--------|------|-------------|
| ip_id | INT | Foreign key to ip_metadata |
| hour_bucket | DATETIME | Hour timestamp |
| inflow | INT | New listings count |
| outflow | INT | Sold items count |
| liquidity_index | FLOAT | outflow / inflow |

### item_snapshots
Individual item tracking.

| Column | Type | Description |
|--------|------|-------------|
| source_id | VARCHAR | Mercari item ID |
| ip_id | INT | Associated IP |
| status | ENUM | on_sale, sold |
| price | INT | Price in JPY |
| first_seen | DATETIME | First crawl time |
| last_seen | DATETIME | Last crawl time |

## Troubleshooting

### Crawler not processing tasks

```bash
# Check queue depth
redis-cli LLEN animetop:queue:tasks

# Check py-crawler Pod status (K8s)
kubectl get pods -n animehot -l app=py-crawler

# Check py-crawler logs (K8s)
kubectl logs -n animehot -l app=py-crawler --tail=100

# Check Spot nodes
kubectl get nodes -l node-role=spot
```

### Metrics not showing in Grafana

```bash
# Check Alloy DaemonSet status
kubectl get ds -n animehot alloy

# Check Alloy logs
kubectl logs -n animehot -l app=alloy --tail=50

# Verify up metric
# In Grafana: up{app="py-crawler", cluster="animehot-k3s"}
```

### Spot nodes not launching

```bash
# Check KEDA ScaledObject
kubectl get scaledobject -n animehot

# Check spot-asg-scaler logs
kubectl logs -n animehot -l app=spot-asg-scaler --tail=50

# Check EC2 ASG status
aws autoscaling describe-auto-scaling-groups --auto-scaling-group-names animehot-spot
```

## License

MIT

## Acknowledgments

- [Mercari](https://jp.mercari.com/) - Data source
- [go-rod](https://github.com/go-rod/rod) - Browser automation
- [Gin](https://github.com/gin-gonic/gin) - Web framework
- [Grafana](https://grafana.com/) - Monitoring
- [Tailscale](https://tailscale.com/) - VPN for distributed crawlers
- [K3s](https://k3s.io/) - Lightweight Kubernetes
- [KEDA](https://keda.sh/) - Kubernetes auto-scaling
