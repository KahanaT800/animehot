#!/bin/bash
# deploy/certbot/init-letsencrypt.sh
# Anime Hot - SSL 证书初始化脚本
#
# 使用方法:
#   chmod +x deploy/certbot/init-letsencrypt.sh
#   ./deploy/certbot/init-letsencrypt.sh
#
# 前置条件:
#   1. 域名已指向服务器 IP
#   2. 80 端口可访问
#   3. 已配置 .env 文件

set -e

# =============================================================================
# 配置
# =============================================================================

# 从环境变量或参数获取
DOMAIN=${DOMAIN_NAME:-anime-hot.com}
EMAIL=${LETSENCRYPT_EMAIL:-admin@$DOMAIN}
STAGING=${LETSENCRYPT_STAGING:-0}  # 设为 1 使用测试环境

# 路径
COMPOSE_FILE="docker-compose.prod.yml"
NGINX_CONF_DIR="deploy/nginx/conf.d"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# =============================================================================
# 检查前置条件
# =============================================================================

log_info "检查前置条件..."

# 检查 docker compose
if ! command -v docker &> /dev/null; then
    log_error "Docker 未安装"
    exit 1
fi

# 检查 compose 文件
if [ ! -f "$COMPOSE_FILE" ]; then
    log_error "找不到 $COMPOSE_FILE"
    exit 1
fi

# 检查 nginx 配置模板
if [ ! -f "$NGINX_CONF_DIR/default.conf.initial" ]; then
    log_error "找不到 $NGINX_CONF_DIR/default.conf.initial"
    exit 1
fi

log_info "域名: $DOMAIN"
log_info "邮箱: $EMAIL"

if [ "$STAGING" = "1" ]; then
    log_warn "使用 Let's Encrypt 测试环境"
fi

# =============================================================================
# Step 1: 生成初始 Nginx 配置 (仅 HTTP)
# =============================================================================

log_info "Step 1: 生成初始 Nginx 配置..."

export DOMAIN_NAME="$DOMAIN"
envsubst '${DOMAIN_NAME}' < "$NGINX_CONF_DIR/default.conf.initial" > "$NGINX_CONF_DIR/default.conf"

log_info "已生成 $NGINX_CONF_DIR/default.conf"

# =============================================================================
# Step 2: 启动基础服务
# =============================================================================

log_info "Step 2: 启动基础服务 (MySQL, Redis, Analyzer, Nginx)..."

docker compose -f "$COMPOSE_FILE" up -d mysql redis
log_info "等待数据库就绪..."
sleep 10

docker compose -f "$COMPOSE_FILE" up -d analyzer
log_info "等待 Analyzer 就绪..."
sleep 5

docker compose -f "$COMPOSE_FILE" up -d nginx
log_info "等待 Nginx 就绪..."
sleep 3

# 验证 Nginx 是否启动
if ! docker compose -f "$COMPOSE_FILE" ps nginx | grep -q "Up"; then
    log_error "Nginx 启动失败"
    docker compose -f "$COMPOSE_FILE" logs nginx
    exit 1
fi

log_info "基础服务已启动"

# =============================================================================
# Step 3: 申请 SSL 证书
# =============================================================================

log_info "Step 3: 申请 SSL 证书..."

# 构建 certbot 参数
CERTBOT_ARGS="certonly --webroot --webroot-path=/var/www/certbot"
CERTBOT_ARGS="$CERTBOT_ARGS -d $DOMAIN"
CERTBOT_ARGS="$CERTBOT_ARGS --email $EMAIL"
CERTBOT_ARGS="$CERTBOT_ARGS --agree-tos --no-eff-email"

if [ "$STAGING" = "1" ]; then
    CERTBOT_ARGS="$CERTBOT_ARGS --staging"
fi

# 运行 certbot
docker compose -f "$COMPOSE_FILE" run --rm certbot $CERTBOT_ARGS

if [ $? -ne 0 ]; then
    log_error "证书申请失败"
    exit 1
fi

log_info "SSL 证书申请成功"

# =============================================================================
# Step 4: 切换到 SSL 配置
# =============================================================================

log_info "Step 4: 切换到 SSL 配置..."

envsubst '${DOMAIN_NAME}' < "$NGINX_CONF_DIR/default.conf.ssl" > "$NGINX_CONF_DIR/default.conf"

log_info "已更新 Nginx 配置为 SSL 模式"

# =============================================================================
# Step 5: 重新加载 Nginx
# =============================================================================

log_info "Step 5: 重新加载 Nginx..."

docker compose -f "$COMPOSE_FILE" exec nginx nginx -t
if [ $? -ne 0 ]; then
    log_error "Nginx 配置检查失败"
    exit 1
fi

docker compose -f "$COMPOSE_FILE" exec nginx nginx -s reload

log_info "Nginx 已重新加载"

# =============================================================================
# Step 6: 启动 Crawler 和 Certbot 续期服务
# =============================================================================

log_info "Step 6: 启动剩余服务..."

docker compose -f "$COMPOSE_FILE" up -d crawler certbot

# =============================================================================
# 完成
# =============================================================================

echo ""
log_info "=========================================="
log_info "SSL 配置完成!"
log_info "=========================================="
echo ""
log_info "访问: https://$DOMAIN"
echo ""
log_info "查看状态: docker compose -f $COMPOSE_FILE ps"
log_info "查看日志: docker compose -f $COMPOSE_FILE logs -f"
echo ""

if [ "$STAGING" = "1" ]; then
    log_warn "当前使用测试证书，正式部署请重新运行:"
    log_warn "  LETSENCRYPT_STAGING=0 ./deploy/certbot/init-letsencrypt.sh"
fi
