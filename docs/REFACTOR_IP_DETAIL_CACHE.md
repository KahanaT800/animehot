# IP 详情页缓存重构计划

## 目标

将 IP 详情页的所有数据统一改为：
1. 使用 **1 小时前的完整聚合数据**（与排行榜口径一致）
2. 采用 **Redis 缓存 + 主动失效** 策略（与排行榜一致）

## 当前状态

| 数据 | API | 数据来源 | 缓存 |
|------|-----|----------|------|
| HOT Score | `/ips/:id/liquidity` | Pipeline 实时写入 Redis | ✅ Redis (实时) |
| SUPPLY-DEMAND GAP (12H) | `/ips/:id/stats/hourly` | MySQL 直查 | ❌ 无 |
| PRICE DISTRIBUTION (12H) | `/ips/:id/stats/hourly` | MySQL 直查 | ❌ 无 |
| RECENT ITEMS | `/ips/:id/items` | MySQL 直查 | ❌ 无 |

## 目标状态

| 数据 | API | 数据来源 | 缓存 | TTL |
|------|-----|----------|------|-----|
| HOT Score | `/ips/:id/liquidity` | MySQL 小时表 | ✅ Redis | 10min |
| SUPPLY-DEMAND GAP (12H) | `/ips/:id/stats/hourly` | MySQL 小时表 | ✅ Redis | 10min |
| PRICE DISTRIBUTION (12H) | `/ips/:id/stats/hourly` | MySQL 小时表 | ✅ Redis | 10min |
| RECENT ITEMS | `/ips/:id/items` | MySQL item_snapshots | ✅ Redis | 10min |

## 缓存设计

### Key 设计

```
animetop:ip:{ip_id}:liquidity      # 流动性指标 (HOT Score)
animetop:ip:{ip_id}:hourly_stats   # 小时级统计 (12H 图表)
animetop:ip:{ip_id}:items          # 商品列表
```

### TTL 策略

- 所有 IP 详情缓存：**10 分钟**
- 原因：小时数据变化频率较低，但需要相对及时的更新

### 失效时机

| 事件 | 失效的缓存 |
|------|-----------|
| Pipeline 处理完成 | 该 IP 的所有详情缓存 |

## 修改文件清单

### Phase 1: 添加缓存基础设施

#### 1.1 `internal/analyzer/cache.go`

添加 IP 详情缓存常量和失效方法：

```go
const (
    // IP 详情缓存
    ipDetailCacheKeyPrefix = "animetop:ip"
    ipDetailCacheTTL       = 10 * time.Minute
)

// InvalidateIPDetailCache 失效指定 IP 的所有详情缓存
func (m *LeaderboardCacheManager) InvalidateIPDetailCache(ctx context.Context, ipID uint64) error {
    keys := []string{
        fmt.Sprintf("%s:%d:liquidity", ipDetailCacheKeyPrefix, ipID),
        fmt.Sprintf("%s:%d:hourly_stats", ipDetailCacheKeyPrefix, ipID),
        fmt.Sprintf("%s:%d:items", ipDetailCacheKeyPrefix, ipID),
    }
    return m.rdb.Del(ctx, keys...).Err()
}
```

### Phase 2: 重构流动性 API

#### 2.1 `internal/api/stats_handler.go` - `getIPLiquidity`

**改动**：
- 移除从 Pipeline 获取实时数据的逻辑
- 改为查询 MySQL `ip_stats_hourly` 表最新记录
- 添加 Redis 缓存读写

**新逻辑**：
```
1. 尝试从 Redis 缓存读取
2. 缓存 miss → 查询 MySQL 小时表最新记录
3. 计算 HOT Score: (outflow+1)/(inflow+1) × log(outflow+1)
4. 异步写入缓存
5. 返回响应
```

**响应格式** (保持不变)：
```json
{
  "ip_id": 11,
  "ip_name": "鬼滅の刃",
  "on_sale_inflow": 355,
  "on_sale_outflow": 28,
  "liquidity_index": 0.079,
  "hot_score": 0.263,
  "updated_at": "2026-01-19T20:00:00+09:00",
  "from_cache": true
}
```

### Phase 3: 添加小时统计缓存

#### 3.1 `internal/api/stats_handler.go` - `getIPHourlyStats`

**改动**：
- 添加 Redis 缓存读写
- 缓存 key 包含查询参数 (limit)

**缓存 Key**：
```
animetop:ip:{ip_id}:hourly_stats:{limit}
例: animetop:ip:11:hourly_stats:12
```

**新逻辑**：
```
1. 尝试从 Redis 缓存读取
2. 缓存 miss → 查询 MySQL
3. 异步写入缓存
4. 返回响应
```

### Phase 4: 添加商品列表缓存

#### 4.1 `internal/api/stats_handler.go` - `getIPItems`

**改动**：
- 添加 Redis 缓存读写
- 缓存 key 包含查询参数 (status, page, page_size)

**缓存 Key**：
```
animetop:ip:{ip_id}:items:{status}:{page}:{page_size}
例: animetop:ip:11:items:on_sale:1:50
```

**新逻辑**：
```
1. 尝试从 Redis 缓存读取
2. 缓存 miss → 查询 MySQL
3. 异步写入缓存
4. 返回响应
```

### Phase 5: Pipeline 缓存失效

#### 5.1 `internal/analyzer/pipeline.go` - `processResult`

**改动**：
- 移除 `CacheLiquidity` 调用
- 添加 `InvalidateIPDetailCache` 调用

```go
// Phase 4: 缓存失效
if p.cacheManager != nil {
    go func() {
        // 失效 1H 排行榜缓存
        _ = p.cacheManager.InvalidateHourlyLeaderboard(context.Background())
        // 失效该 IP 的详情缓存
        _ = p.cacheManager.InvalidateIPDetailCache(context.Background(), ipID)
    }()
}
```

### Phase 6: 清理旧代码

#### 6.1 `internal/analyzer/diff_engine.go`

**删除**：
- `CacheLiquidity()` 方法
- `GetCachedLiquidity()` 方法
- `cacheLiquidityScript` Lua 脚本
- `keyLiquidityFmt` 常量

#### 6.2 `internal/analyzer/pipeline.go`

**删除**：
- `GetIPLiquidity()` 导出方法
- `CacheLiquidity()` 导出方法
- Phase 2 中的 `CacheLiquidity` 调用

#### 6.3 `internal/api/stats_handler.go`

**删除**：
- `getIPLiquidity` 中的 Pipeline 依赖

### Phase 7: 清理 Redis 旧数据

```bash
# 删除旧的流动性缓存
docker exec animetop-redis redis-cli KEYS "animetop:liquidity:*" | xargs -r docker exec -i animetop-redis redis-cli DEL
```

## 执行顺序

1. **Phase 1**: 添加缓存基础设施 (cache.go)
2. **Phase 2**: 重构流动性 API
3. **Phase 3**: 添加小时统计缓存
4. **Phase 4**: 添加商品列表缓存
5. **Phase 5**: Pipeline 缓存失效
6. **Phase 6**: 清理旧代码
7. **Phase 7**: 清理 Redis 旧数据
8. **构建测试**: `go build ./...`
9. **部署验证**

## 验证步骤

```bash
# 1. 构建
go build -v ./cmd/analyzer

# 2. 部署
docker compose build analyzer && docker compose up -d --force-recreate analyzer

# 3. 测试流动性 API
curl -s "http://localhost:8080/api/v1/ips/11/liquidity" | jq .

# 4. 测试小时统计 API (带缓存)
curl -s "http://localhost:8080/api/v1/ips/11/stats/hourly?limit=12" | jq '{from_cache: .data.from_cache, count: .data.count}'

# 5. 测试商品列表 API (带缓存)
curl -s "http://localhost:8080/api/v1/ips/11/items?page=1&page_size=10" | jq '{from_cache: .data.from_cache, total: .data.total}'

# 6. 验证缓存 key
docker exec animetop-redis redis-cli KEYS "animetop:ip:*"

# 7. 确认旧缓存已清除
docker exec animetop-redis redis-cli KEYS "animetop:liquidity:*"
```

## 回滚方案

如果需要回滚，重新部署之前的镜像即可，数据库结构无变更。

## 风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 数据延迟增加 | 最多 1 小时延迟 | 与排行榜一致，用户可接受 |
| 缓存击穿 | 高并发时 MySQL 压力 | 10 分钟 TTL + 异步回写 |
| 缓存 key 过多 | Redis 内存占用 | 每个 IP 最多 3 个 key，可控 |
