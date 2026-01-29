# Python Mercari 爬虫开发计划

## 1. 项目背景

### 1.1 问题分析

当前 Go 浏览器爬虫方案存在以下问题：

| 问题 | 影响 |
|------|------|
| 内存占用高 | 10 并发 ≈ 2.5GB，限制了单机部署密度 |
| 浏览器资源重 | 每次请求都需要完整浏览器渲染 |
| 扩展性差 | 增加并发 = 线性增加内存 |

### 1.2 解决方案

基于 DPoP 逆向工程，采用**双模式架构** (HTTP 优先 + 浏览器回退)：

```
┌─────────────────────────────────────────────────────────────────┐
│                     双模式架构                                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│   ┌──────────────────────────────────────────────────────────┐ │
│   │                   Auth Manager                           │ │
│   │                                                          │ │
│   │   ┌────────────────┐         ┌────────────────┐         │ │
│   │   │   HTTP 模式     │  失败   │   浏览器模式    │         │ │
│   │   │   (DPoP 自生成) │ ──────► │   (Playwright) │         │ │
│   │   │                │         │                │         │ │
│   │   │  - 无浏览器依赖 │  恢复   │  - 完整 Token  │         │ │
│   │   │  - 极低内存     │ ◄────── │  - Cookies 捕获│         │ │
│   │   └────────────────┘         └────────────────┘         │ │
│   │                                                          │ │
│   └──────────────────────────────────────────────────────────┘ │
│                              │                                  │
│                              ▼                                  │
│   ┌──────────────┐      TLS指纹模拟      ┌──────────────┐      │
│   │   curl_cffi  │  ◄───────────────────  │  API Client  │      │
│   │   (轻量HTTP) │                       └──────────────┘      │
│   └──────────────┘                                              │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**核心优势**：
- 内存：~300MB（纯 HTTP 模式，vs 2.5GB）
- 启动快：无需预热浏览器
- 稳定性：双模式自动切换，无单点故障
- 扩展性好：HTTP 请求可高并发

---

## 2. 技术架构

### 2.1 系统架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Go Analyzer                                  │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐             │
│  │  Scheduler  │───►│ Task Queue  │    │Result Queue │───►Pipeline │
│  └─────────────┘    └──────┬──────┘    └──────▲──────┘             │
└────────────────────────────┼─────────────────┼─────────────────────┘
                             │     Redis       │
                             ▼                 │
┌────────────────────────────────────────────────────────────────────┐
│                      Python Crawler                                 │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐             │
│  │   Engine    │───►│ Rate Limiter│───►│  API Client │             │
│  │  (主循环)    │    │ (共享令牌桶) │    │ (curl_cffi) │             │
│  └─────────────┘    └─────────────┘    └──────┬──────┘             │
│                                               │                     │
│                     ┌─────────────────────────┴──────────────┐      │
│                     │          Auth Manager                  │      │
│                     │  ┌───────────┐    ┌───────────────┐   │      │
│                     │  │   HTTP    │    │    Browser    │   │      │
│                     │  │   DPoP    │◄──►│   Playwright  │   │      │
│                     │  │  (优先)   │    │    (回退)     │   │      │
│                     │  └───────────┘    └───────────────┘   │      │
│                     └────────────────────────────────────────┘      │
└────────────────────────────────────────────────────────────────────┘
```

### 2.2 技术栈

| 功能 | 库 | 选型理由 |
|------|-----|---------|
| HTTP 客户端 | `curl_cffi` | Chrome TLS 指纹模拟，绕过 JA3 检测 |
| 浏览器自动化 | `playwright` + `playwright-stealth` | Token 捕获 + 反检测 |
| 重试/退避 | `tenacity` | 成熟的重试库，支持指数退避 |
| 熔断器 | `aiobreaker` | 异步熔断器，防止级联故障 |
| 限流 | Redis Lua | 与 Go 共享全局令牌桶 |
| 配置 | `pydantic-settings` | 类型安全 + 环境变量 |
| 日志 | `structlog` | JSON 结构化日志 |
| 指标 | `prometheus-client` | Prometheus 兼容 |
| 健康检查 | `aiohttp` | 轻量 HTTP 服务 |

### 2.3 模块设计

```
py-crawler/
├── src/mercari_crawler/
│   ├── __init__.py          # 版本信息
│   ├── main.py              # 入口 + 健康检查服务
│   ├── config.py            # Pydantic Settings 配置
│   ├── models.py            # CrawlRequest/Response 数据类
│   ├── redis_queue.py       # Redis 队列客户端
│   ├── rate_limiter.py      # Redis 全局令牌桶
│   ├── dpop_generator.py    # DPoP Token 生成器 (纯 HTTP)
│   ├── auth_manager.py      # 双模式认证管理器
│   ├── request_template.py  # API 请求体模板
│   ├── api_client.py        # Mercari API + 重试 + 熔断
│   ├── engine.py            # 爬虫主循环
│   └── metrics.py           # Prometheus 指标
├── demos/                   # 验证原型脚本
├── configs/config.yaml      # 默认配置
├── Dockerfile
├── docker-compose.yml
└── pyproject.toml
```

### 2.4 双模式认证架构

通过 DPoP 逆向工程，实现了完全无浏览器依赖的 HTTP 模式：

```
┌─────────────────────────────────────────────────────────────────────┐
│                     双模式认证架构                                    │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│   ┌────────────────────────────────────────────────────────────┐   │
│   │                    AuthManager                              │   │
│   │                                                             │   │
│   │   ┌─────────────────┐      ┌─────────────────┐             │   │
│   │   │   HTTP 模式      │      │   浏览器模式     │             │   │
│   │   │   (优先)         │      │   (回退)        │             │   │
│   │   │                 │      │                 │             │   │
│   │   │  DPoPGenerator  │      │   Playwright    │             │   │
│   │   │  - EC P-256 密钥 │      │   + Stealth     │             │   │
│   │   │  - JWT 签名      │      │   + Cookie 捕获  │             │   │
│   │   └────────┬────────┘      └────────┬────────┘             │   │
│   │            │                         │                      │   │
│   │            │    连续 3 次失败         │                      │   │
│   │            └────────────────────────►│                      │   │
│   │                                      │                      │   │
│   │            │    5 分钟后尝试恢复      │                      │   │
│   │            ◄─────────────────────────┘                      │   │
│   │                                                             │   │
│   └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**DPoP 逆向要点**：
- 算法：ES256 (ECDSA with P-256 and SHA-256)
- 密钥：EC P-256 密钥对，每 15 分钟轮换
- JWT Payload：`iat`, `jti`, `htu`, `htm`, `uuid`
- 无需浏览器：完全在 Python 中生成有效的 DPoP token

**双模式切换条件**：
| 事件 | 动作 |
|------|------|
| HTTP 模式连续 3 次失败 | 切换到浏览器模式 |
| 403 错误 | 进入 60 秒冷却期 |
| 浏览器模式 5 分钟后 | 尝试恢复 HTTP 模式 |
| DPoP 密钥满 15 分钟 | 自动轮换密钥 |

---

## 3. 核心设计

### 3.1 与 Go 服务的兼容性

#### Redis 队列协议

```python
# 必须与 Go internal/pkg/redisqueue/client.go 保持一致
KEY_TASK_QUEUE = "animetop:queue:tasks"
KEY_TASK_PROCESSING = "animetop:queue:tasks:processing"
KEY_TASK_PENDING_SET = "animetop:queue:tasks:pending"
KEY_RESULT_QUEUE = "animetop:queue:results"
```

#### 消息格式

使用 protojson 格式（camelCase + 数字字符串）：

```json
{
  "ipId": "123",
  "keyword": "hololive",
  "taskId": "550e8400-e29b-41d4-a716-446655440000",
  "createdAt": "1706500000",
  "pagesOnSale": 5,
  "pagesSold": 5
}
```

#### 限流共享

复用 Go 的 Lua 脚本，确保全局限流生效：

```python
# 与 Go internal/pkg/ratelimit/ratelimit.go 完全相同
TOKEN_BUCKET_LUA = """
local key = KEYS[1]
local rate = tonumber(ARGV[1])
...
"""
```

### 3.2 认证管理策略

#### HTTP 模式 (DPoP) - 优先

```
DPoP 密钥生命周期：

  ├────────────── 15 分钟 ─────────────┤
  │                                    │
  │◄──────── 正常使用 ─────────────────►│
  │                                    │
  生成                                轮换
```

- **DPoP 生成**：使用 EC P-256 密钥对签名 JWT
- **密钥轮换**：每 15 分钟自动生成新的密钥对
- **零依赖**：完全不需要浏览器

#### 浏览器模式 - 回退

```
Browser Token 生命周期：

  ├────────────── max_age (30 min) ──────────────┤
  │                                              │
  │◄─── 95% 正常使用 ───►│◄─── 5% 主动刷新 ───►│
  │                      │                       │
  获取                   刷新点                  过期
```

- **触发条件**：HTTP 模式连续 3 次失败
- **恢复机制**：5 分钟后尝试切换回 HTTP 模式
- **冷却保护**：403 后 60 秒内不发送请求

### 3.3 容错机制

```
请求流程：

  Request ──► Rate Limiter ──► Circuit Breaker ──► Retry ──► API
                  │                   │              │
                  ▼                   ▼              ▼
              等待令牌            熔断保护       指数退避
              (共享桶)          (5次失败)      (5s-300s)
```

| 机制 | 配置 | 说明 |
|------|------|------|
| 限流 | 5 req/s, burst=10 | 与 Go 共享，防止触发 Mercari 429 |
| 重试 | 3 次，指数退避 | 仅针对网络错误，非业务错误 |
| 熔断 | 5 次失败后熔断 60s | 防止持续请求失败的 API |
| Jitter | 1-5s 随机延迟 | 避免 Thundering Herd |

---

## 4. 配置说明

### 4.1 配置优先级

```
环境变量 > YAML 文件 > 默认值
```

### 4.2 关键配置项

```yaml
# configs/config.yaml
redis:
  addr: "localhost:6379"      # REDIS_ADDR 环境变量覆盖

rate_limit:
  rate: 5                     # APP_RATE_LIMIT (必须与 Go 一致!)
  burst: 10                   # APP_RATE_BURST (必须与 Go 一致!)

token:
  max_age_minutes: 30         # Token 有效期
  proactive_refresh_ratio: 0.05  # 提前刷新比例

crawler:
  max_concurrent_tasks: 3     # 最大并发任务数
```

---

## 5. 监控与可观测性

### 5.1 健康检查端点

| 端点 | 用途 |
|------|------|
| `GET /health` | 详细健康状态 (JSON) |
| `GET /healthz` | 简单存活检查 |
| `GET /ready` | 就绪检查 |
| `GET /metrics` | Prometheus 指标 |

### 5.2 关键指标

```
# 任务处理
mercari_crawler_tasks_processed_total{status="success|error"}
mercari_crawler_tasks_in_progress
mercari_crawler_task_duration_seconds

# API 调用
mercari_crawler_api_requests_total{status="success|rate_limited|forbidden"}
mercari_crawler_circuit_breaker_state

# 认证模式
mercari_crawler_auth_mode              # 0=HTTP, 1=Browser
mercari_crawler_auth_mode_switches_total{direction="to_browser|to_http"}
mercari_crawler_auth_consecutive_failures
mercari_crawler_dpop_key_age_seconds   # DPoP 密钥年龄

# Token (浏览器模式)
mercari_crawler_token_age_seconds
mercari_crawler_token_refreshes_total
```

---

## 6. 部署架构

### 6.1 单机部署

```
┌─────────────────────────────────────────┐
│              Docker Host                 │
│  ┌─────────────┐    ┌─────────────┐     │
│  │ Go Analyzer │◄──►│    Redis    │     │
│  │  (port 8080)│    │ (port 6379) │     │
│  └─────────────┘    └──────▲──────┘     │
│                            │            │
│  ┌─────────────────────────┴──────────┐ │
│  │         Python Crawler             │ │
│  │  Health: 8081  Metrics: 2113       │ │
│  └────────────────────────────────────┘ │
└─────────────────────────────────────────┘
```

### 6.2 分布式部署

```
┌──────────────────┐     ┌──────────────────┐
│   Main Server    │     │  Crawler Node    │
│  ┌────────────┐  │     │  ┌────────────┐  │
│  │ Go Analyzer│  │     │  │ Py Crawler │  │
│  └────────────┘  │     │  └────────────┘  │
│  ┌────────────┐  │     │                  │
│  │   Redis    │◄─┼─────┼──────────────────┤
│  └────────────┘  │     │   (Tailscale)    │
└──────────────────┘     └──────────────────┘
```

---

## 7. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| HTTP 模式 DPoP 失效 | API 返回 401/403 | 自动回退到浏览器模式，5分钟后尝试恢复 |
| Mercari 更新反爬 | API 调用失败 | 监控 403/429 比例，双模式提供冗余 |
| curl_cffi 指纹过期 | 被检测为爬虫 | 跟踪 Chrome 版本，更新 impersonate |
| Redis 连接断开 | 任务丢失 | 可靠消费 + 自动重连 |
| DPoP 密钥泄露 | 可能被追踪 | 每 15 分钟轮换密钥 + 随机 UUID |
| IP 封禁 | 所有请求失败 | 分布式部署 + 代理池 (可选) |

---

## 8. 参考文档

- [Go Redis Queue 实现](../internal/pkg/redisqueue/client.go)
- [Go Rate Limiter 实现](../internal/pkg/ratelimit/ratelimit.go)
- [Protobuf 消息定义](../proto/crawler.proto)
- [混合模式验证脚本](./demos/mercari_hybrid_test.py)
