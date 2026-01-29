# Demo Scripts

验证脚本，用于测试 Python 爬虫核心功能。

## 脚本说明

| 脚本 | 用途 | 运行方式 |
|------|------|----------|
| `dpop_full_test.py` | DPoP 完全独立测试 - 验证无需浏览器即可调用 API | `python dpop_full_test.py` |
| `concurrent_stress_test.py` | 并发压力测试 - 模拟生产场景 | `python concurrent_stress_test.py` |
| `auth_dual_mode_test.py` | 双模式认证测试 - 验证 HTTP/浏览器回退机制 | `python auth_dual_mode_test.py` |

## 前置条件

```bash
# 安装依赖
pip install -r requirements-api-test.txt

# 如需浏览器回退测试
pip install playwright playwright-stealth
playwright install chromium
```

## dpop_full_test.py

验证 DPoP token 可以自己生成，完全脱离浏览器：

```
DPoP 完全独立测试 - 不使用浏览器
======================================================================

→ 生成 DPoP...
  Device UUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx

测试: 随机 sessionId (UUID hex)
  ✓ 成功: 120 商品

连续请求测试 (同一个 DPoP generator)
  请求 1: ✓ 95 商品
  请求 2: ✓ 120 商品
  ...

结果: 5/5 成功
```

## concurrent_stress_test.py

模拟生产环境的并发压力测试：

- 3 个并发任务
- 每任务 5 页 on_sale + 5 页 sold
- 9 个测试关键词

```
配置:
  并发任务数: 3
  每任务页数: 10 页
  预计请求数: 90 个

测试结果:
  总耗时: 180s
  总商品: 8500
  成功率: 95%
  吞吐量: 3.0 任务/分钟
```

## auth_dual_mode_test.py

测试实际模块的双模式认证：

- HTTP 模式 (DPoP) 正常工作
- 连续失败后自动回退到浏览器模式
- 多页爬取测试

```bash
# 需要先安装主模块
cd ..
pip install -e .

# 运行测试
cd demos
python auth_dual_mode_test.py
```
