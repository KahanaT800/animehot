# Nginx 配置说明

## 文件结构

```
deploy/nginx/
├── nginx.conf                    # 主配置文件
├── conf.d/
│   ├── default.conf.initial      # 初始配置 (仅 HTTP，用于证书申请)
│   └── default.conf.ssl          # SSL 配置 (完整生产配置)
└── README.md
```

## 部署步骤

### 1. 首次部署 (获取 SSL 证书前)

```bash
# 复制初始配置
cp deploy/nginx/conf.d/default.conf.initial deploy/nginx/conf.d/default.conf

# 替换域名占位符
export DOMAIN_NAME=your-domain.com
envsubst '${DOMAIN_NAME}' < deploy/nginx/conf.d/default.conf.initial > deploy/nginx/conf.d/default.conf

# 启动服务
docker compose -f docker-compose.prod.yml up -d nginx
```

### 2. 申请 SSL 证书

```bash
# 运行 certbot
docker compose -f docker-compose.prod.yml run --rm certbot certonly \
    --webroot \
    --webroot-path=/var/www/certbot \
    -d your-domain.com \
    --email your-email@example.com \
    --agree-tos \
    --no-eff-email
```

### 3. 切换到 SSL 配置

```bash
# 生成 SSL 配置
export DOMAIN_NAME=your-domain.com
envsubst '${DOMAIN_NAME}' < deploy/nginx/conf.d/default.conf.ssl > deploy/nginx/conf.d/default.conf

# 重新加载 Nginx
docker compose -f docker-compose.prod.yml exec nginx nginx -s reload
```

## 配置说明

### 速率限制

| 区域 | 限制 | 突发 | 说明 |
|------|------|------|------|
| `api_limit` | 10r/s | 20 | API 端点 `/api/*` |
| `general_limit` | 30r/s | 50 | 其他路由 |
| `conn_limit` | 10 连接 | - | 每 IP 最大连接数 |

### 安全特性

- **HTTPS 强制**: HTTP 自动重定向到 HTTPS
- **HSTS**: 2 年有效期，包含子域名，preload
- **TLS 1.2+**: 禁用旧版本 TLS
- **现代密码套件**: Mozilla Intermediate 配置
- **/metrics 保护**: 仅允许内部网络访问

### 允许访问 /metrics 的网段

- `10.0.0.0/8` - Docker 内部网络
- `172.16.0.0/12` - Docker 桥接网络
- `192.168.0.0/16` - 本地网络
- `100.64.0.0/10` - Tailscale CGNAT

## 环境变量

| 变量 | 说明 | 示例 |
|------|------|------|
| `DOMAIN_NAME` | 域名 | `anime-hot.com` |

## 日志

日志位置 (容器内):
- 访问日志: `/var/log/nginx/access.log`
- 错误日志: `/var/log/nginx/error.log`

日志格式支持:
- `main`: 标准文本格式 (默认)
- `json`: JSON 格式 (便于日志分析)

## 故障排查

### 检查配置语法
```bash
docker compose -f docker-compose.prod.yml exec nginx nginx -t
```

### 查看日志
```bash
docker compose -f docker-compose.prod.yml logs nginx
```

### 重新加载配置
```bash
docker compose -f docker-compose.prod.yml exec nginx nginx -s reload
```

### 测试 SSL 配置
```bash
# 使用 SSL Labs 测试 (在线)
# https://www.ssllabs.com/ssltest/

# 本地测试
curl -I https://your-domain.com
openssl s_client -connect your-domain.com:443 -servername your-domain.com
```
