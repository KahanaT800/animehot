# 本地全量测试指南

从容器构建到结果确认的完整流程。

## 目录

1. [环境检查](#1-环境检查)
2. [构建镜像](#2-构建镜像)
3. [启动服务](#3-启动服务)
4. [导入测试数据](#4-导入测试数据)
5. [功能验证](#5-功能验证)
6. [数据流验证](#6-数据流验证)
7. [增量抓取验证](#7-增量抓取验证)
8. [性能监控](#8-性能监控)
9. [故障排查](#9-故障排查)
10. [清理环境](#10-清理环境)

---

## 1. 环境检查

### 1.1 检查 Docker

```bash
# 检查 Docker 版本
docker --version
docker compose version

# 检查 Docker 服务状态
docker info
```

### 1.2 检查端口占用

```bash
# 确保以下端口未被占用
# 3306 - MySQL
# 6379 - Redis
# 8080 - API Server

lsof -i :3306 || echo "3306 可用"
lsof -i :6379 || echo "6379 可用"
lsof -i :8080 || echo "8080 可用"
```

### 1.3 检查配置文件

```bash
# 确认 .env 文件存在
cat .env | head -20

# 关键配置检查
grep -E "^(MYSQL_|REDIS_|BROWSER_|APP_)" .env
```

---

## 2. 构建镜像

### 2.1 构建所有镜像

```bash
# 构建 analyzer, crawler, import 三个镜像
make docker-build

# 或单独构建
docker compose build analyzer
docker compose build crawler
docker compose build import
```

### 2.2 验证镜像

```bash
# 查看构建的镜像
docker images | grep animetop

# 预期输出:
# animetop-analyzer   latest   xxx   xx seconds ago   xxMB
# animetop-crawler    latest   xxx   xx seconds ago   xxMB
# animetop-import     latest   xxx   xx seconds ago   xxMB
```

---

## 3. 启动服务

### 3.1 启动基础设施

```bash
# 先启动 MySQL 和 Redis
docker compose up -d mysql redis

# 等待 MySQL 就绪 (约 30 秒)
echo "等待 MySQL 启动..."
sleep 10
docker compose exec mysql mysqladmin ping -u root -p${MYSQL_ROOT_PASSWORD:-animetop_root_pass} --wait=30

# 检查服务状态
docker compose ps
```

### 3.2 启动应用服务

```bash
# 启动 Analyzer (API + Scheduler + Pipeline)
docker compose up -d analyzer

# 等待 Analyzer 就绪
sleep 5
curl -s http://localhost:8080/health || echo "Analyzer 未就绪"

# 启动 Crawler
docker compose up -d crawler

# 查看所有服务状态
docker compose ps
```

### 3.3 一键启动 (替代方案)

```bash
# 启动所有服务
docker compose up -d

# 等待就绪
sleep 30
docker compose ps
```

---

## 4. 导入测试数据

### 4.1 预览模式

```bash
# 查看将要导入的数据
docker compose run --rm import -file /app/data/ips_grayscale.json -dry-run
```

### 4.2 执行导入

```bash
# 导入测试 IP
docker compose run --rm import -file /app/data/ips_grayscale.json

# 预期输出:
# Imported 2 IPs successfully
# - 初音ミク (ID: 1)
# - 葬送のフリーレン (ID: 2)
```

### 4.3 验证导入

```bash
# 通过 API 查看
curl -s http://localhost:8080/api/v1/ips | jq '.data[] | {id, name, weight, status}'

# 通过 MySQL 查看
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop \
  -e "SELECT id, name, weight, status FROM ip_metadata;"
```

---

## 5. 功能验证

### 5.1 API 端点测试

```bash
echo "=== 5.1.1 健康检查 ==="
curl -s http://localhost:8080/health | jq

echo -e "\n=== 5.1.2 IP 列表 ==="
curl -s http://localhost:8080/api/v1/ips | jq

echo -e "\n=== 5.1.3 IP 详情 ==="
curl -s http://localhost:8080/api/v1/ips/1 | jq

echo -e "\n=== 5.1.4 系统状态 ==="
curl -s http://localhost:8080/api/v1/system/status | jq

echo -e "\n=== 5.1.5 调度器状态 ==="
curl -s http://localhost:8080/api/v1/system/scheduler | jq
```

### 5.2 调度器验证

```bash
# 查看调度器日志
docker compose logs analyzer 2>&1 | grep -i "schedul" | tail -20

# 预期看到:
# level=info msg="Scheduler initialized"
# level=info msg="IP scheduled" ip_id=1 next_run=...
```

### 5.3 手动触发抓取

```bash
# 触发 IP 1 (初音ミク)
echo "触发 IP 1..."
curl -s -X POST http://localhost:8080/api/v1/ips/1/trigger | jq

# 触发 IP 2 (葬送のフリーレン)
echo "触发 IP 2..."
curl -s -X POST http://localhost:8080/api/v1/ips/2/trigger | jq
```

---

## 6. 数据流验证

### 6.1 检查 Redis 队列

```bash
echo "=== 6.1.1 任务队列 ==="
docker compose exec redis redis-cli LLEN animetop:tasks:pending
docker compose exec redis redis-cli LRANGE animetop:tasks:pending 0 -1

echo -e "\n=== 6.1.2 处理中队列 ==="
docker compose exec redis redis-cli HLEN animetop:tasks:processing

echo -e "\n=== 6.1.3 结果队列 ==="
docker compose exec redis redis-cli LLEN animetop:results:pending
```

### 6.2 监控 Crawler 日志

```bash
# 实时查看 Crawler 日志
docker compose logs -f crawler

# 预期看到:
# level=info msg="crawler worker started"
# level=info msg="fetching items" ip_id=1 keyword="初音ミク"
# level=info msg="crawl completed" items_found=xxx
```

### 6.3 监控 Pipeline 日志

```bash
# 实时查看 Analyzer 日志 (包含 Pipeline)
docker compose logs -f analyzer

# 预期看到:
# level=info msg="Processing crawl response" ip_id=1
# level=info msg="Snapshot saved" ip_id=1 items=xxx
# level=info msg="Computed diff" inflow=xxx outflow=xxx
```

### 6.4 检查 MySQL 数据

```bash
echo "=== 6.4.1 IP 元数据 ==="
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop \
  -e "SELECT id, name, status, last_crawled_at FROM ip_metadata;"

echo -e "\n=== 6.4.2 小时级统计 ==="
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop \
  -e "SELECT ip_id, hour_bucket, inflow, outflow, liquidity_index FROM ip_stats_hourly ORDER BY hour_bucket DESC LIMIT 10;"

echo -e "\n=== 6.4.3 商品快照 ==="
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop \
  -e "SELECT ip_id, COUNT(*) as count, status FROM item_snapshots GROUP BY ip_id, status;"

echo -e "\n=== 6.4.4 预警记录 ==="
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop \
  -e "SELECT * FROM ip_alerts ORDER BY created_at DESC LIMIT 5;"
```

### 6.5 检查 Redis 快照

```bash
echo "=== 6.5.1 快照 Keys ==="
docker compose exec redis redis-cli KEYS "animetop:snapshot:*"

echo -e "\n=== 6.5.2 IP 1 当前快照大小 ==="
docker compose exec redis redis-cli SCARD "animetop:snapshot:1:current"

echo -e "\n=== 6.5.3 锚点窗口 ==="
docker compose exec redis redis-cli LRANGE "animetop:anchor:1:on_sale" 0 -1
```

---

## 7. 增量抓取验证

### 7.1 首次抓取确认

```bash
# 检查首次抓取标记
docker compose logs analyzer 2>&1 | grep -i "first.*crawl" | tail -5

# 检查锚点初始化
docker compose logs analyzer 2>&1 | grep -i "anchor.*init" | tail -5
```

### 7.2 等待并触发第二次抓取

```bash
# 等待一段时间或手动触发
echo "等待 60 秒后触发第二次抓取..."
sleep 60

# 手动触发
curl -s -X POST http://localhost:8080/api/v1/ips/1/trigger | jq
```

### 7.3 验证增量逻辑

```bash
# 查看增量抓取日志
docker compose logs analyzer 2>&1 | grep -i "incremental\|anchor\|stopped" | tail -10

# 预期看到:
# level=info msg="incremental crawl filtered" total_crawled=xxx new_items=xxx stopped_at=xxx
# 或
# level=info msg="anchor found" page=0 anchor=mxxxxxxx
```

### 7.4 对比两次抓取

```bash
# 查看统计变化
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop \
  -e "SELECT ip_id, hour_bucket, inflow, outflow,
      ROUND(liquidity_index, 2) as liquidity
      FROM ip_stats_hourly
      WHERE ip_id = 1
      ORDER BY hour_bucket DESC
      LIMIT 5;"
```

---

## 8. 性能监控

### 8.1 容器资源使用

```bash
# 实时监控
docker stats --no-stream

# 持续监控 (Ctrl+C 退出)
docker stats
```

### 8.2 预期资源使用

| 服务 | 内存限制 | 正常范围 |
|------|---------|---------|
| mysql | 512MB | 200-400MB |
| redis | 192MB | 30-100MB |
| analyzer | 256MB | 80-150MB |
| crawler | 768MB | 300-600MB |

### 8.3 检查服务健康

```bash
# 检查所有服务
docker compose ps

# 检查重启次数
docker compose ps --format "table {{.Name}}\t{{.Status}}"
```

---

## 9. 故障排查

### 9.1 服务未启动

```bash
# 查看失败原因
docker compose logs <service_name> --tail=50

# 常见问题:
# - 端口占用: lsof -i :<port>
# - 内存不足: free -h
# - 配置错误: docker compose config
```

### 9.2 数据库连接失败

```bash
# 测试 MySQL 连接
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} -e "SELECT 1;"

# 检查表是否存在
docker compose exec mysql mysql -u animetop -p${MYSQL_PASSWORD:-animetop_pass} animetop -e "SHOW TABLES;"
```

### 9.3 Redis 连接失败

```bash
# 测试 Redis 连接
docker compose exec redis redis-cli PING
```

### 9.4 Crawler 抓取失败

```bash
# 检查详细日志
docker compose logs crawler --tail=100

# 常见错误:
# - "blocked": 被反爬检测，检查代理配置
# - "timeout": 网络问题或页面加载慢
# - "browser not initialized": Chromium 启动失败
```

### 9.5 Pipeline 处理失败

```bash
# 检查 Pipeline 日志
docker compose logs analyzer 2>&1 | grep -i "error\|fail" | tail -20
```

---

## 10. 清理环境

### 10.1 停止服务 (保留数据)

```bash
docker compose down
```

### 10.2 停止并删除数据

```bash
# 删除容器和卷
docker compose down -v

# 删除镜像
docker compose down --rmi local
```

### 10.3 完全清理

```bash
# 删除所有相关资源
docker compose down -v --rmi local --remove-orphans

# 清理悬空镜像
docker image prune -f
```

---

## 快速命令参考

```bash
# 一键启动全栈
make grayscale-start

# 一键验证
make grayscale-verify

# 查看日志
make docker-logs              # 所有服务
docker compose logs -f analyzer  # Analyzer
docker compose logs -f crawler   # Crawler

# 进入数据库
make db-shell                 # MySQL
make redis-cli                # Redis

# 触发抓取
curl -X POST http://localhost:8080/api/v1/ips/1/trigger

# 检查数据
make grayscale-check-mysql
make grayscale-check-redis

# 停止服务
make grayscale-stop

# 清理全部
make grayscale-clean
```

---

## 测试检查清单

完成以下检查项表示测试成功:

- [ ] Docker 镜像构建成功
- [ ] 所有服务启动无错误
- [ ] MySQL 表结构正确创建
- [ ] 测试 IP 导入成功
- [ ] API 端点响应正常
- [ ] 调度器按预期运行
- [ ] Crawler 成功抓取数据
- [ ] Pipeline 处理结果正确
- [ ] MySQL 有统计数据 (`ip_stats_hourly`)
- [ ] MySQL 有商品数据 (`item_snapshots`)
- [ ] Redis 有快照数据
- [ ] 增量抓取逻辑生效
- [ ] 内存使用在限制内
- [ ] 无异常重启或崩溃
