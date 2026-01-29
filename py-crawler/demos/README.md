# Mercari API 验证原型

这些脚本用于验证混合模式爬虫方案的可行性。

## 脚本说明

| 脚本 | 用途 | 结论 |
|------|------|------|
| `mercari_hybrid_test.py` | **核心验证** - 浏览器获取 Token + HTTP 调用 API | ✅ 可行，30 请求 100% 成功 |
| `mercari_token_test.py` | 测试 Token 有效期 | ✅ Token 至少 5 分钟有效 |
| `mercari_api_direct_test.py` | 直接 API 调用测试 | ⚠️ 需要有效 Token |
| `mercari_api_test.py` | 完整 API 测试套件 | 参考实现 |

## 运行方式

```bash
# 安装依赖
pip install -r requirements-api-test.txt
pip install playwright playwright-stealth
playwright install chromium

# 运行混合模式测试 (推荐)
python mercari_hybrid_test.py

# 测试 Token 有效期
python mercari_token_test.py
```

## 核心发现

### 1. 混合模式可行

```
浏览器 (一次性) → 获取 Headers + Cookies
         ↓
HTTP (持续) → curl_cffi 调用 API → 成功!
```

### 2. 关键技术点

- **TLS 指纹**: 必须使用 `curl_cffi` 模拟 Chrome 指纹
- **Headers**: 需要完整复制浏览器请求头
- **Cookies**: 需要携带 Mercari session cookies

### 3. 内存对比

| 方案 | 10 并发内存 |
|------|------------|
| 纯浏览器 | ~2.5GB |
| 混合模式 | ~300MB |

## 验证结果

`mercari_hybrid_test.py` 输出示例：

```
============================================================
Mercari 混合模式测试
浏览器获取认证 → HTTP 调用 API
============================================================

Step 1: 浏览器捕获认证信息

访问: https://jp.mercari.com/search?keyword=hololive&status=on_sale
[捕获] POST https://api.mercari.jp/v2/entities:search...
  x-platform: web
  dpop: eyJhbGciOi...

提取到 5 个 cookies

成功捕获 API 请求参数!

Step 2: 用 curl_cffi 调用 API (关键词: 初音ミク)

POST https://api.mercari.jp/v2/entities:search
Status: 200

==================================================
成功! 找到 30 个商品!
==================================================

商品 1:
  ID: m12345678
  名称: 初音ミク フィギュア
  价格: ¥3500
  状态: ITEM_STATUS_ON_SALE
```

## 下一步

基于这些验证结果，已实现完整的 Python 爬虫节点：

- 代码: `../src/mercari_crawler/`
- 文档: `../docs/DEVELOPMENT.md`
- 部署: `../docs/DEPLOYMENT.md`
