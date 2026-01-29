# Python Mercari 爬虫实施计划

## 1. 实施阶段

### Phase 1: 本地开发与测试 ✅

| 任务 | 状态 | 说明 |
|------|------|------|
| 项目结构搭建 | ✅ 完成 | pyproject.toml, 目录结构 |
| 配置模块 | ✅ 完成 | Pydantic Settings |
| 数据模型 | ✅ 完成 | CrawlRequest/Response |
| Redis 队列 | ✅ 完成 | 与 Go 兼容的协议 |
| 限流模块 | ✅ 完成 | 共享 Lua 脚本 |
| Token 管理 | ✅ 完成 | Playwright + stealth |
| API 客户端 | ✅ 完成 | curl_cffi + tenacity + aiobreaker |
| 爬虫引擎 | ✅ 完成 | 主循环 + 优雅关闭 |
| 监控指标 | ✅ 完成 | Prometheus metrics |
| 健康检查 | ✅ 完成 | aiohttp server |
| Docker 配置 | ✅ 完成 | Dockerfile + compose |

### Phase 2: 集成测试 (待执行)

```bash
# 2.1 启动依赖服务
cd /home/lyc/animehot
make dev-deps  # 启动 MySQL + Redis

# 2.2 启动 Go Analyzer (另一个终端)
make dev-web

# 2.3 启动 Python Crawler
cd py-crawler
make dev       # 安装依赖
make run       # 启动爬虫

# 2.4 验证任务流转
# Go Scheduler 推送任务 → Python Crawler 消费 → 结果返回 Go Pipeline
```

### Phase 3: 性能验证 (待执行)

| 指标 | 目标 | 验证方法 |
|------|------|----------|
| 内存占用 | < 500MB | `docker stats` |
| Token 成功率 | > 95% | Prometheus metrics |
| API 成功率 | > 99% | 监控 403/429 比例 |
| 任务吞吐 | ≥ 5 req/s | 观察队列消费速度 |

### Phase 4: 生产部署 (待执行)

见下文详细步骤。

---

## 2. 本地开发指南

### 2.1 环境准备

```bash
# 安装 Python 3.11+
python3 --version  # 确认 >= 3.11

# 安装 Poetry
curl -sSL https://install.python-poetry.org | python3 -

# 进入项目目录
cd /home/lyc/animehot/py-crawler

# 安装依赖
make dev

# 这会执行:
# poetry install --with dev
# poetry run playwright install chromium
```

### 2.2 运行验证脚本

```bash
# 测试混合模式 (推荐先运行这个)
cd demos
python mercari_hybrid_test.py

# 测试 Token 有效期
python mercari_token_test.py

# 测试直接 API 调用
python mercari_api_direct_test.py
```

### 2.3 本地运行爬虫

```bash
# 方式1: 直接运行 (需要 Redis)
make run

# 方式2: Docker Compose (包含 Redis)
make docker-up
make docker-logs
```

### 2.4 检查健康状态

```bash
# 健康检查
curl http://localhost:8081/health | jq

# 期望输出:
{
  "healthy": true,
  "redis": "ok",
  "token": "valid",
  "circuit_breaker": "closed",
  "active_tasks": 0,
  "running": true
}

# Prometheus 指标
curl http://localhost:2113/metrics
```

---

## 3. 集成测试步骤

### 3.1 准备测试环境

```bash
# 终端 1: 启动 Redis + MySQL
cd /home/lyc/animehot
make dev-deps

# 终端 2: 启动 Go Analyzer
make dev-web
# 访问 http://localhost:8080 确认启动成功

# 终端 3: 启动 Python Crawler
cd py-crawler
REDIS_ADDR=localhost:6379 make run
```

### 3.2 手动推送测试任务

```bash
# 使用 redis-cli 推送测试任务
redis-cli

# 在 redis-cli 中执行:
SADD animetop:queue:tasks:pending "ip:999"
LPUSH animetop:queue:tasks '{"ipId":"999","keyword":"hololive","taskId":"test-001","createdAt":"1706500000","pagesOnSale":1,"pagesSold":1}'
```

### 3.3 验证任务处理

```bash
# 观察 Python Crawler 日志
# 应该看到:
# {"event": "task_popped", "task_id": "test-001", "ip_id": 999, ...}
# {"event": "task_completed", "items": 120, "pages": 2, ...}

# 检查结果队列
redis-cli LLEN animetop:queue:results
# 应该返回 >= 1

# 查看结果内容
redis-cli LRANGE animetop:queue:results 0 0
```

### 3.4 验证 Go Pipeline 消费

```bash
# 观察 Go Analyzer 日志
# 应该看到 Pipeline 处理结果的日志

# 检查 MySQL 数据
mysql -u root -p animetop -e "SELECT * FROM item_snapshots ORDER BY id DESC LIMIT 5;"
```

---

## 4. Docker 部署

### 4.1 构建镜像

```bash
cd /home/lyc/animehot/py-crawler

# 构建镜像
docker build -t mercari-py-crawler:latest .

# 或使用 make
make docker-build
```

### 4.2 开发环境部署

```bash
# 启动 (包含 Redis)
docker-compose -f docker-compose.dev.yml up -d

# 查看日志
docker-compose -f docker-compose.dev.yml logs -f py-crawler

# 停止
docker-compose -f docker-compose.dev.yml down
```

### 4.3 生产环境部署

```bash
# 创建网络 (如果不存在)
docker network create animetop-network

# 启动 (连接外部 Redis)
REDIS_ADDR=redis:6379 docker-compose up -d

# 或者指定远程 Redis
REDIS_ADDR=10.0.0.100:6379 REDIS_PASSWORD=secret docker-compose up -d
```

---

## 5. 生产部署清单

### 5.1 部署前检查

- [ ] Redis 连接可达
- [ ] 网络配置正确 (animetop-network)
- [ ] 环境变量设置正确
- [ ] 限流配置与 Go Analyzer 一致

### 5.2 部署步骤

```bash
# 1. 拉取最新代码
cd /home/lyc/animehot
git pull

# 2. 构建镜像
cd py-crawler
docker build -t mercari-py-crawler:latest .

# 3. 停止旧容器 (如有)
docker stop mercari-py-crawler || true
docker rm mercari-py-crawler || true

# 4. 启动新容器
docker run -d \
  --name mercari-py-crawler \
  --network animetop-network \
  --restart unless-stopped \
  -e REDIS_ADDR=redis:6379 \
  -e APP_RATE_LIMIT=5 \
  -e APP_RATE_BURST=10 \
  -p 8081:8081 \
  -p 2113:2113 \
  --memory=1g \
  mercari-py-crawler:latest

# 5. 验证健康
sleep 30
curl http://localhost:8081/health
```

### 5.3 部署后验证

```bash
# 检查容器状态
docker ps | grep mercari-py-crawler

# 检查内存使用
docker stats mercari-py-crawler --no-stream

# 检查日志
docker logs -f mercari-py-crawler --tail 100

# 检查指标
curl -s http://localhost:2113/metrics | grep mercari_crawler
```

---

## 6. 监控告警配置

### 6.1 Prometheus 配置

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'py-crawler'
    static_configs:
      - targets: ['py-crawler:2113']
    scrape_interval: 15s
```

### 6.2 告警规则

```yaml
# alerts.yml
groups:
  - name: py-crawler
    rules:
      # Token 过期告警
      - alert: CrawlerTokenExpired
        expr: mercari_crawler_token_age_seconds > 1800
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Python Crawler token expired"

      # 熔断器打开告警
      - alert: CrawlerCircuitBreakerOpen
        expr: mercari_crawler_circuit_breaker_state == 1
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Python Crawler circuit breaker is open"

      # 任务处理失败率告警
      - alert: CrawlerHighErrorRate
        expr: |
          rate(mercari_crawler_tasks_processed_total{status="error"}[5m])
          / rate(mercari_crawler_tasks_processed_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Python Crawler error rate > 10%"
```

---

## 7. 故障排查

### 7.1 常见问题

| 问题 | 症状 | 解决方案 |
|------|------|----------|
| Token 捕获失败 | 日志显示 "token_capture_failed" | 检查网络，可能被 Cloudflare 拦截 |
| Redis 连接失败 | "redis not connected" | 检查 REDIS_ADDR 配置 |
| 403 Forbidden | API 返回 403 | Token 过期或被封，等待自动刷新 |
| 429 Rate Limited | API 返回 429 | 检查限流配置是否与 Go 一致 |
| 熔断器打开 | circuit_breaker_state=1 | 等待 60s 自动恢复，检查 Mercari 状态 |

### 7.2 调试命令

```bash
# 查看详细日志
docker logs mercari-py-crawler 2>&1 | jq

# 检查 Redis 队列状态
redis-cli
> LLEN animetop:queue:tasks
> LLEN animetop:queue:tasks:processing
> LLEN animetop:queue:results

# 检查限流状态
redis-cli HMGET animetop:ratelimit:global tokens ts

# 手动触发健康检查
curl -v http://localhost:8081/health
```

### 7.3 紧急回滚

```bash
# 停止 Python Crawler
docker stop mercari-py-crawler

# Go Crawler 会自动接管 (如果还在运行)
# 或者重新启动 Go Crawler
cd /home/lyc/animehot
docker-compose up -d crawler
```

---

## 8. 维护计划

### 8.1 日常维护

| 任务 | 频率 | 说明 |
|------|------|------|
| 检查健康状态 | 每日 | `curl /health` |
| 检查内存使用 | 每日 | `docker stats` |
| 检查错误率 | 每日 | Prometheus dashboard |
| 清理日志 | 每周 | Docker 日志轮转 |

### 8.2 版本更新

```bash
# 1. 拉取新代码
git pull

# 2. 重新构建
docker build -t mercari-py-crawler:latest .

# 3. 滚动更新
docker stop mercari-py-crawler
docker rm mercari-py-crawler
# 重新启动 (见 5.2)
```

### 8.3 依赖更新

```bash
# 更新依赖
cd py-crawler
poetry update

# 测试
make test

# 重新构建镜像
make docker-build
```

---

## 9. 联系与支持

- **代码仓库**: `/home/lyc/animehot/py-crawler`
- **验证脚本**: `/home/lyc/animehot/py-crawler/demos/`
- **Go 参考实现**: `/home/lyc/animehot/internal/crawler/`
- **Proto 定义**: `/home/lyc/animehot/proto/crawler.proto`
