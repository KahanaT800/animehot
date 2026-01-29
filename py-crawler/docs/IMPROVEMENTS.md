# Python 爬虫改进方向

## 当前方案评估

| 维度 | 当前状态 | 评分 |
|------|----------|------|
| 功能 | Token 捕获 + API 调用成功 | ✅ |
| 内存 | ~500MB (vs Go 2.5GB) | ✅ |
| 稳定性 | 依赖完整请求体模板 | ⚠️ |
| 维护性 | Mercari 更新可能需要适配 | ⚠️ |

---

## 改进方向优先级

### P0: 当前方案优化（短期）

#### 1. 研究最小请求体

```bash
# 测试哪些字段是必须的
cd py-crawler/demos
python mercari_minimal_body_test.py
```

需要测试的字段：
- [ ] `searchSessionId` - 是否必须？能否随机生成？
- [ ] `config` - 是否必须？
- [ ] `indexRouting` - 是否必须？
- [ ] `source` - 是否必须？

#### 2. Token 有效期优化

当前设置 30 分钟刷新，但 demo 测试显示可能更长：

```python
# 测试更长的有效期
TOKEN_MAX_AGE_MINUTES = 60  # 尝试 60 分钟
```

#### 3. 请求体缓存

```python
# 请求体模板可以持久化，减少刷新时的字段变化
class TokenManager:
    async def capture(self):
        ...
        # 只更新 headers/cookies，保留请求体模板
        if not self._auth.request_body:
            self._auth.request_body = captured.request_body
```

---

### P1: 浏览器优化（中期）

#### 方案 A: Camoufox（推荐）

```python
# Camoufox - 基于 Firefox，更好的反检测
from camoufox.async_api import AsyncCamoufox

async with AsyncCamoufox() as browser:
    page = await browser.new_page()
    # 内置反检测，无需 stealth 插件
```

**优点**：
- 比 Chromium 更轻量
- 内置更强的反检测
- 支持指纹随机化

**安装**：
```bash
pip install camoufox
camoufox fetch
```

#### 方案 B: 浏览器池

```python
# 预启动浏览器，减少冷启动时间
class BrowserPool:
    def __init__(self, size=2):
        self._browsers = []

    async def get_browser(self):
        # 复用已启动的浏览器
        ...
```

---

### P2: API 逆向（长期）

#### DPoP Token 分析

DPoP (Demonstrating Proof of Possession) 是 OAuth 2.0 扩展：

```
DPoP Header 格式: eyJ0eXAiOiJkcG9wK2p3dCIsImFsZyI6IkVTMjU2IiwiandrIj...

解码后:
{
  "typ": "dpop+jwt",
  "alg": "ES256",
  "jwk": {
    "kty": "EC",
    "crv": "P-256",
    "x": "...",
    "y": "..."
  }
}
```

**逆向步骤**：
1. 分析 Mercari 前端 JS，找到 DPoP 生成逻辑
2. 提取密钥生成和签名算法
3. 用 Python 重新实现

**难度**：高，JS 可能混淆

#### searchSessionId 分析

```python
# 当前从浏览器捕获
"searchSessionId": "8d1439bf75fc4f0a5c9725170ea1e8d9"

# 如果是简单的 UUID/hash，可以自己生成
import hashlib
session_id = hashlib.md5(f"{timestamp}{random}".encode()).hexdigest()
```

---

### P3: 架构优化（长期）

#### 1. 多 Token 轮换

```python
class TokenPool:
    """维护多个有效 Token，轮换使用"""

    def __init__(self, pool_size=3):
        self._tokens: list[CapturedAuth] = []

    async def get_token(self) -> CapturedAuth:
        # 轮换返回，分散请求
        ...
```

#### 2. 分布式 Token 共享

```python
# 多个爬虫节点共享 Token (存 Redis)
class RedisTokenStore:
    async def save_token(self, auth: CapturedAuth):
        await redis.hset("mercari:tokens", auth.id, auth.to_json())

    async def get_valid_token(self) -> CapturedAuth:
        # 获取任意有效 token
        ...
```

---

## 风险评估

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Mercari 更新请求体格式 | 中 | 高 | 监控 400 错误，自动告警 |
| DPoP 算法变更 | 低 | 高 | 保持浏览器备用方案 |
| TLS 指纹被检测 | 低 | 中 | 跟踪 curl_cffi 更新 |
| IP 被封 | 中 | 中 | 代理池 + 限流 |

---

## 建议的实施顺序

```
Week 1-2: P0 当前方案优化
  ├── 测试最小请求体
  ├── 优化 Token 有效期
  └── 部署生产验证

Week 3-4: P1 浏览器优化
  ├── 评估 Camoufox
  └── 实现浏览器池（如需要）

Month 2+: P2/P3 深度优化
  ├── API 逆向研究
  └── 分布式 Token 共享
```

---

## 快速实验脚本

### 测试最小请求体

```python
# demos/mercari_minimal_body_test.py
async def test_minimal_body():
    """测试最小必需字段"""

    # 基础字段
    minimal = {
        "keyword": "hololive",
        "status": ["ITEM_STATUS_ON_SALE"],
        "pageSize": 30,
    }

    # 逐个添加字段测试
    optional_fields = [
        ("searchSessionId", uuid.uuid4().hex),
        ("sort", "SORT_CREATED_TIME"),
        ("order", "ORDER_DESC"),
    ]

    for field, value in optional_fields:
        body = minimal.copy()
        body[field] = value
        result = await test_request(body)
        print(f"{field}: {'✓' if result.ok else '✗'}")
```

### 测试 Token 有效期

```python
# demos/mercari_token_lifetime_test.py
async def test_token_lifetime():
    """测试 Token 实际有效期"""

    # 捕获 token
    token = await capture_token()

    # 每 5 分钟测试一次
    for minutes in range(0, 120, 5):
        await asyncio.sleep(300)
        result = await test_api(token)
        print(f"{minutes} min: {'✓' if result.ok else '✗ EXPIRED'}")
        if not result.ok:
            break
```
