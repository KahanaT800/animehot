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

你的二次元婆罗门论战利器：实时追踪日本二手市场上各大动漫IP的流动性指数，看看谁在入坑、谁在退坑！

## 这是什么？

作为一个二次元婆罗门，你是不是也想知道：
- 哪些番才是正在的国民级IP
- 哪些番的粉丝开始退坑了？二手市场被周边淹没...（会赢的！）
- 入手某个IP的周边，是抄底良机还是高位接盘？
- 鬼马咒万家到底谁是德不配位（

### [🔥 立刻看看哪些番最火 →](https://anime-hot.com)

**Anime Hot** 通过分析 [煤炉](https://jp.mercari.com/)（日本最大的二手交易平台）上的动漫周边交易数据，计算每个IP的"流动性指数"，帮你洞察市场趋势！

### 核心指标

| 指标 | 计算公式 | 人话解释 |
|------|---------|---------|
| **流入量** | 每小时新上架数 | 有多少人在出周边（可能在退坑） |
| **流出量** | 每小时成交数 | 有多少人在收周边 |
| **流动性指数** | 流出 / 流入 | 越大越强！ |
| **热度分** | (Out+1)/(In+1) × log(Out+1) | 综合热度评分 |

### 能干啥？

- **追番风向标**: 哪些番正在出圈？哪些番凉了？
- **韭菜预警器**: 某IP周边价格虚高？还是捡漏良机？
- **退坑观察站**: 大量周边涌入二手市场，粉丝们这是怎么了...（我打宿傩？！）

## 截图预览

<p align="center">
  <img src="docs/screenshots/dashboard.png" alt="排行榜" width="800">
  <br>
  <em>实时热番排行榜，看看谁是本季霸权！</em>
</p>

<p align="center">
  <img src="docs/screenshots/ip-detail.png" alt="IP详情" width="800">
  <br>
  <em>单个IP的详细流动性分析</em>
</p>

<p align="center">
  <img src="docs/screenshots/grafana.png" alt="Grafana监控" width="800">
  <br>
  <em>Grafana Cloud 监控面板（运维狂喜）</em>
</p>

## 系统架构

### 整体架构

```mermaid
graph TB
    CLIENT[浏览器]

    subgraph EC2[AWS EC2 - 主节点 / K3s Server]
        NGINX[Nginx :80/:443]
        ANALYZER[Analyzer :8080]
        MYSQL[(MySQL)]
        REDIS[(Redis)]
        KEDA[KEDA]
        SCALER[Spot ASG Scaler]
    end

    subgraph SPOT[AWS Spot 节点池 - K3s Agent]
        CRAWLER1[py-crawler Pod]
        CRAWLER2[py-crawler Pod]
        ALLOY[Alloy DaemonSet]
    end

    MERCARI[煤炉]
    GRAFANA[Grafana Cloud]
    ASG[EC2 Auto Scaling Group]

    CLIENT --> NGINX
    NGINX --> ANALYZER
    ANALYZER <--> MYSQL
    ANALYZER <--> REDIS

    KEDA -->|队列深度| SCALER
    SCALER -->|调整容量| ASG
    ASG -->|启动/终止| SPOT

    CRAWLER1 & CRAWLER2 -.->|Tailscale VPN| REDIS
    CRAWLER1 & CRAWLER2 --> MERCARI

    ALLOY --> GRAFANA
```

### 核心设计

- **K3s 集群**: EC2 主节点作为 Server，Spot 实例作为 Agent 节点
- **KEDA 自动扩缩容**: 根据 Redis 队列深度自动调整 py-crawler 副本数
- **Spot ASG Scaler**: 根据 Pod pending 状态启动 Spot 节点，空闲时自动终止
- **Tailscale VPN**: Spot 节点通过 Tailscale 连接主节点的 Redis
- **Grafana Alloy**: DaemonSet 方式部署，自动收集所有节点的指标和日志

### 任务流程

```mermaid
sequenceDiagram
    participant ZSET as Redis ZSET
    participant SCH as 调度器
    participant RQ as Redis 队列
    participant CRW as 爬虫 (任意节点)
    participant SM as 状态机
    participant PIP as 处理管道
    participant DB as MySQL

    SCH->>ZSET: 查询最近调度时间
    SCH->>SCH: 精确睡眠到该时间
    SCH->>ZSET: 获取到期的IP
    SCH->>RQ: 推送爬取任务

    loop 爬虫循环
        CRW->>RQ: 拉取任务 (BRPOP)
        CRW->>CRW: HTTP 认证请求
        CRW->>CRW: 爬取在售页面 x5
        CRW->>CRW: 爬取已售页面 x5
        CRW->>RQ: 推送结果
    end

    PIP->>RQ: 拉取结果
    PIP->>SM: 批量处理商品
    SM->>SM: 检测状态变化 (新上架/卖出/改价)
    SM-->>PIP: 返回变化记录
    PIP->>PIP: 计算指标 (流入/流出/流动性)
    PIP->>DB: 写入小时统计
    PIP->>ZSET: 闭环更新下次调度时间
    PIP->>RQ: 清除相关缓存
```

## 技术栈

- **后端**: Go 1.24+ (Gin + GORM)
- **爬虫**: Python 3.11+ (HTTP 认证 + Playwright 降级)
- **消息格式**: Protocol Buffers (protojson)
- **数据库**: MySQL 8.0 + Redis 7.x
- **监控**: Prometheus + Grafana Cloud + Alloy

## 快速开始

### 前置要求

- Docker & Docker Compose
- Go 1.24+

### 本地开发

```bash
# 克隆仓库
git clone https://github.com/KahanaT800/animehot
cd animehot

# 复制环境配置
cp .env.example .env

# 启动基础设施 (MySQL + Redis)
make dev-deps

# 启动分析器 (终端1)
make dev-analyzer

# 启动爬虫 (终端2)
make dev-crawler

# 导入测试IP
make api-import-run FILE=data/ips.json
```

### Docker 部署

```bash
# 全家桶 (MySQL + Redis + Analyzer + Crawler)
make docker-up

# 轻量模式 (不带爬虫，纯测试用)
make docker-light-up

# 带监控 (Grafana Cloud)
make docker-up-monitoring

# 查看日志
make docker-logs
```

## 生产部署

### EC2 初始化

```bash
# 1. 安装 Docker
sudo yum update -y
sudo yum install -y docker git
sudo systemctl start docker && sudo systemctl enable docker
sudo usermod -aG docker ec2-user

# 2. 安装 Docker Compose
sudo curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64" \
  -o /usr/local/bin/docker-compose
sudo chmod +x /usr/local/bin/docker-compose

# 3. 安装 Tailscale (用于分布式爬虫)
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up

# 4. 克隆并配置
git clone https://github.com/lyc0603/animetop.git
cd animetop
cp .env.example .env
# 编辑 .env 填入生产配置

# 5. 初始化 SSL 并启动服务
export DOMAIN_NAME=your-domain.com
export LETSENCRYPT_EMAIL=admin@your-domain.com
./deploy/certbot/init-letsencrypt.sh
```

### 安全检查清单

- [ ] MySQL 端口 (3306) 没有对外暴露
- [ ] Redis 只能通过 Tailscale VPN 访问
- [ ] Admin API 用 `ADMIN_API_KEY` 保护
- [ ] 强制 HTTPS + HSTS
- [ ] `/metrics` 端点禁止外部访问

## K8s/Spot 分布式爬虫

使用 AWS Spot 实例按需扩展爬虫容量，成本仅为按需实例的 10-30%。

### 架构说明

```
EC2 主节点 (K3s Server)          Spot 节点池 (K3s Agent)
┌─────────────────────┐          ┌─────────────────────┐
│  Analyzer           │          │  py-crawler Pod     │
│  MySQL / Redis      │◄─────────│  py-crawler Pod     │
│  KEDA               │ Tailscale│  Alloy DaemonSet    │
│  Spot ASG Scaler    │          └─────────────────────┘
└─────────────────────┘                    ▲
         │                                 │
         ▼                                 │
   ┌───────────┐     调整容量      ┌───────────────┐
   │ 队列深度  │ ─────────────────▶│  EC2 ASG      │
   └───────────┘                   └───────────────┘
```

### 自动扩缩容逻辑

| 触发条件 | 动作 |
|---------|------|
| 队列深度 > 0 | KEDA 创建 py-crawler Pod |
| Pod pending (无节点) | Scaler 启动 Spot 实例 |
| 节点空闲 15 分钟 | Scaler 终止 Spot 实例 |
| Spot 中断通知 | 优雅关闭 Pod，节点自动清理 |

### K3s 集群初始化

```bash
# EC2 主节点 - 安装 K3s Server
curl -sfL https://get.k3s.io | sh -s - server \
  --tls-san $(tailscale ip -4) \
  --node-external-ip $(tailscale ip -4)

# 获取 join token
cat /var/lib/rancher/k3s/server/node-token

# 部署 K8s 资源
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/secrets.yaml  # 需要先填入凭据
kubectl apply -f k8s/py-crawler.yaml
kubectl apply -f k8s/keda-scaledobject.yaml
kubectl apply -f k8s/spot-asg-scaler.yaml
kubectl apply -f k8s/alloy-configmap.yaml
kubectl apply -f k8s/alloy-daemonset.yaml
```

### Spot 节点配置

Spot 实例启动时自动执行 `infra/aws/user-data-spot.sh`:

1. 安装 Tailscale 并加入网络
2. 加入 K3s 集群作为 Agent 节点
3. 设置 Tailscale 清理 hook (终止时自动注销)
4. 监听 Spot 中断通知

### 关键 K8s 资源

| 文件 | 说明 |
|------|------|
| `k8s/py-crawler.yaml` | py-crawler Deployment |
| `k8s/keda-scaledobject.yaml` | KEDA 自动扩缩容规则 |
| `k8s/spot-asg-scaler.yaml` | Spot 节点管理 CronJob |
| `k8s/alloy-*.yaml` | Grafana Alloy 监控配置 |
| `k8s/secrets.yaml.template` | 凭据模板 |

## 监控

### Grafana Cloud 配置

1. 注册 [Grafana Cloud](https://grafana.com/products/cloud/) 账号
2. 获取 Prometheus remote write 凭据
3. 获取 Loki 凭据 (日志用)
4. 在 `.env` 中配置:

```bash
GRAFANA_CLOUD_PROM_REMOTE_WRITE_URL=https://prometheus-xxx.grafana.net/api/prom/push
GRAFANA_CLOUD_PROM_USERNAME=your_username
GRAFANA_CLOUD_PROM_API_KEY=glc_xxx

GRAFANA_CLOUD_LOKI_URL=https://logs-xxx.grafana.net/loki/api/v1/push
GRAFANA_CLOUD_LOKI_USERNAME=your_username
GRAFANA_CLOUD_LOKI_API_KEY=glc_xxx
```

5. 带监控配置启动:

```bash
docker compose -f docker-compose.prod.yml --profile monitoring up -d
```

### 导入仪表盘

从 `deploy/grafana/dashboards/animehot-business.json` 导入业务仪表盘:

| 区域 | 面板 |
|------|------|
| Overview | 服务状态、活跃任务、队列深度 |
| Spot Py-Crawler | 爬虫数、任务进度、延迟、Auth 模式 |
| Task Queue | 任务吞吐量、队列状态 |
| Redis Queues | DLQ、调度 IP、任务/结果队列 |

### 关键指标

| 指标 | 说明 |
|------|------|
| `up{job="animetop-analyzer"}` | Analyzer 健康状态 |
| `up{app="py-crawler", cluster="animehot-k3s"}` | Spot 爬虫健康状态 |
| `mercari_crawler_tasks_in_progress{cluster="animehot-k3s"}` | Spot 正在处理的任务数 |
| `mercari_crawler_api_request_duration_seconds` | API 请求延迟 |
| `mercari_crawler_auth_mode` | 认证模式 (0=HTTP, 1=Browser) |
| `animetop_scheduler_tasks_pending_in_queue` | 队列深度 |
| `animetop_queue_length{queue="schedule"}` | 已调度的 IP 数量 |

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查 |
| GET | `/api/v1/ips` | 获取所有追踪的IP列表 |
| GET | `/api/v1/ips/:id` | 获取IP详情 |
| GET | `/api/v1/ips/:id/liquidity` | 获取流动性数据 |
| GET | `/api/v1/ips/:id/stats/hourly` | 获取小时统计 |
| GET | `/api/v1/ips/:id/items` | 获取商品列表 |
| GET | `/api/v1/leaderboard` | 获取排行榜 |
| GET | `/api/v1/system/status` | 系统状态 |
| POST | `/api/v1/admin/import` | 导入IP (需要API密钥) |

### 排行榜 API

```bash
# 获取过去24小时热度前10的IP
curl "http://localhost:8080/api/v1/leaderboard?type=hot&hours=24&limit=10"
```

参数:
- `type`: `hot` | `inflow` | `outflow`
- `hours`: 1-168 (时间窗口)
- `limit`: 1-100

响应示例:
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
        "ip_name": "鬼灭之刃",
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
# 导入IP (生产环境需要 X-API-Key 头)
curl -X POST http://localhost:8080/api/v1/admin/import \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -d @data/ips.json
```

## 配置

### 关键环境变量

```bash
# 域名 (SSL用)
DOMAIN_NAME=anime-hot.com
LETSENCRYPT_EMAIL=admin@anime-hot.com

# 安全
ADMIN_API_KEY=your_secure_api_key

# 数据库
MYSQL_PASSWORD=your_secure_password

# 调度器 (ZSET 持久化 + 精确睡眠)
SCHEDULER_BASE_INTERVAL=2h      # 基础爬取间隔
SCHEDULER_MIN_INTERVAL=1h       # 最小间隔 (热门IP)
SCHEDULER_MAX_INTERVAL=2h       # 最大间隔

# 爬虫
BROWSER_MAX_CONCURRENCY=2       # 同时开的浏览器标签页数
SCHEDULER_PAGES_ON_SALE=5       # 每次爬取在售页数
SCHEDULER_PAGES_SOLD=5          # 每次爬取已售页数
```

### 动态间隔调整

调度器会根据活跃度自动调整爬取频率 (闭环更新到 Redis ZSET):

| 条件 | 动作 |
|------|------|
| 流入 > 100×页数 或 流出 > 100×页数 | 加速 (-15分钟) |
| 流入 < 50×页数 且 流出 < 3×页数 | 减速 (+15分钟) |
| 其他情况 | 向2小时回归 |

默认 5+5 页配置下:
- **加速条件**: 流入 > 500 或 流出 > 500
- **减速条件**: 流入 < 250 且 流出 < 15

## Make 命令

```bash
# 构建
make build              # 构建所有 Go 二进制
make test               # 跑测试
make lint               # 跑 linter

# Docker - 全家桶
make docker-up          # 启动所有服务
make docker-down        # 停止所有服务
make docker-logs        # 查看日志

# Docker - 轻量版 (不带爬虫)
make docker-light-up    # 启动 MySQL + Redis + Analyzer
make docker-light-down  # 停止轻量服务

# Docker - 带监控
make docker-up-monitoring    # 启动全部 + Grafana Alloy
make docker-down-monitoring  # 停止全部 + 监控

# 开发
make dev-deps           # 只启动 MySQL & Redis
make dev-analyzer       # 本地跑 Analyzer
make dev-crawler        # 本地跑 Crawler

# 数据导入
make api-import-run FILE=data/ips.json

# 灰度测试
make grayscale-start    # 全家桶 + 测试IP
make grayscale-verify   # 验证数据流
make grayscale-clean    # 清理
```

## 项目结构

```
animetop/
├── cmd/
│   ├── analyzer/          # API + 调度器 + 处理管道
│   ├── crawler/           # Go 爬虫 (deprecated)
│   └── import/            # IP数据导入工具
├── internal/
│   ├── analyzer/          # 核心分析逻辑
│   │   ├── pipeline.go    # 结果处理
│   │   ├── state_machine.go  # 商品状态追踪
│   │   └── cache.go       # 缓存管理
│   ├── api/               # HTTP 接口
│   ├── config/            # 配置
│   ├── model/             # 数据库模型 (GORM)
│   ├── pkg/               # 公共工具库
│   │   ├── metrics/       # Prometheus 指标
│   │   ├── ratelimit/     # 限流
│   │   └── redisqueue/    # 可靠队列
│   └── scheduler/         # IP 调度 (ZSET 持久化)
├── py-crawler/            # Python 爬虫 (私有子模块)
├── k8s/                   # Kubernetes 资源
│   ├── py-crawler.yaml    # 爬虫 Deployment
│   ├── keda-scaledobject.yaml  # KEDA 自动扩缩容
│   ├── spot-asg-scaler.yaml    # Spot 节点管理
│   ├── alloy-*.yaml       # Grafana Alloy 监控
│   └── secrets.yaml.template   # 凭据模板
├── infra/aws/             # AWS 基础设施
│   ├── asg.yaml           # Auto Scaling Group 配置
│   ├── launch-template.json    # EC2 Launch Template
│   ├── user-data-spot.sh  # Spot 实例初始化脚本
│   └── iam-policy.json    # IAM 策略
├── deploy/
│   ├── nginx/             # Nginx 配置
│   ├── certbot/           # SSL 初始化
│   └── grafana/           # 仪表盘 JSON
├── scripts/
│   └── k3s-init.sh        # K3s 集群初始化
├── proto/                 # Protocol Buffers
├── migrations/            # 数据库迁移
├── data/                  # 测试数据 (IP JSON)
├── docker-compose.yml           # 开发环境
├── docker-compose.prod.yml      # 生产环境 (EC2)
└── docker-compose.crawler.yml   # 本地爬虫节点 (备用)
```

## 数据库设计

### ip_metadata
存储 IP（知识产权/动漫作品）信息

| 字段 | 类型 | 说明 |
|------|------|------|
| id | INT | 主键 |
| name | VARCHAR | 日文名 |
| name_en | VARCHAR | 英文名 |
| category | VARCHAR | 分类 (anime, game 等) |
| weight | FLOAT | 调度优先级 |

### ip_stats_hourly
每个IP的小时统计

| 字段 | 类型 | 说明 |
|------|------|------|
| ip_id | INT | 外键关联 ip_metadata |
| hour_bucket | DATETIME | 小时时间戳 |
| inflow | INT | 新上架数 |
| outflow | INT | 成交数 |
| liquidity_index | FLOAT | 流出 / 流入 |

### item_snapshots
单个商品追踪

| 字段 | 类型 | 说明 |
|------|------|------|
| source_id | VARCHAR | 煤炉商品ID |
| ip_id | INT | 关联的IP |
| status | ENUM | on_sale, sold |
| price | INT | 价格 (日元) |
| first_seen | DATETIME | 首次抓取时间 |
| last_seen | DATETIME | 最后抓取时间 |

## 常见问题排查

### 爬虫不处理任务

```bash
# 检查队列深度
redis-cli LLEN animetop:queue:tasks

# 检查 Spot 爬虫 Pod 状态
kubectl get pods -n animehot -l app=py-crawler

# 检查爬虫日志
kubectl logs -n animehot -l app=py-crawler --tail 100

# 检查 KEDA ScaledObject 状态
kubectl get scaledobject -n animehot
```

### Spot 节点没有启动

```bash
# 检查 Scaler 日志
kubectl logs -n kube-system -l app=spot-asg-scaler --tail 50

# 检查 ASG 状态
aws autoscaling describe-auto-scaling-groups \
  --auto-scaling-group-names animehot-spot-asg

# 手动触发扩容 (测试用)
aws autoscaling set-desired-capacity \
  --auto-scaling-group-name animehot-spot-asg \
  --desired-capacity 1
```

### Grafana 看不到 Spot 指标

```bash
# 检查 Alloy Pod 状态
kubectl get pods -n animehot -l app=alloy

# 检查 Alloy 日志
kubectl logs -n animehot daemonset/alloy --tail 50

# 验证 up 指标
# 在 Grafana: up{app="py-crawler", cluster="animehot-k3s"}
```

### Spot 节点加入失败

```bash
# SSH 到 Spot 实例检查日志
journalctl -u k3s-agent -f

# 检查 Tailscale 状态
tailscale status

# 检查 K3s Server 连接
curl -k https://100.99.127.100:6443/healthz
```

## 开源协议

MIT

## 致谢

- [煤炉](https://jp.mercari.com/) - 数据来源
- [Playwright](https://playwright.dev/) - 浏览器自动化
- [Gin](https://github.com/gin-gonic/gin) - Web 框架
- [K3s](https://k3s.io/) - 轻量 Kubernetes
- [KEDA](https://keda.sh/) - 自动扩缩容
- [Grafana](https://grafana.com/) - 监控
- [Tailscale](https://tailscale.com/) - 分布式 VPN
