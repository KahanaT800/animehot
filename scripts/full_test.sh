#!/bin/bash
#
# 本地全量测试脚本
# 用法: ./scripts/full_test.sh [--skip-build] [--quick]
#

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
SKIP_BUILD=false
QUICK_MODE=false
WAIT_CRAWLER=120  # 等待 Crawler 完成的秒数

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        --quick)
            QUICK_MODE=true
            WAIT_CRAWLER=60
            shift
            ;;
        *)
            echo "未知参数: $1"
            echo "用法: $0 [--skip-build] [--quick]"
            exit 1
            ;;
    esac
done

# 辅助函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_section() {
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN} $1${NC}"
    echo -e "${GREEN}========================================${NC}"
}

check_command() {
    if command -v $1 &> /dev/null; then
        log_success "$1 已安装"
        return 0
    else
        log_error "$1 未安装"
        return 1
    fi
}

wait_for_service() {
    local url=$1
    local max_attempts=${2:-30}
    local attempt=0

    while [ $attempt -lt $max_attempts ]; do
        if curl -s "$url" > /dev/null 2>&1; then
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done
    return 1
}

# ============================================================
# 1. 环境检查
# ============================================================
log_section "1. 环境检查"

check_command docker
check_command curl
check_command jq || log_warn "jq 未安装，部分输出可能不够美观"

if ! docker info > /dev/null 2>&1; then
    log_error "Docker 服务未运行"
    exit 1
fi
log_success "Docker 服务正常"

if [ ! -f ".env" ]; then
    log_error ".env 文件不存在"
    exit 1
fi
log_success ".env 文件存在"

# ============================================================
# 2. 构建镜像
# ============================================================
log_section "2. 构建镜像"

if [ "$SKIP_BUILD" = true ]; then
    log_warn "跳过构建 (--skip-build)"
else
    log_info "构建 Docker 镜像 (包含 import 工具)..."
    docker compose --profile tools build
    log_success "镜像构建完成"
fi

# ============================================================
# 3. 清理旧环境
# ============================================================
log_section "3. 清理旧环境"

log_info "停止并清理旧容器..."
docker compose down -v 2>/dev/null || true
log_success "旧环境已清理"

# ============================================================
# 4. 启动服务
# ============================================================
log_section "4. 启动服务"

log_info "启动 MySQL 和 Redis..."
docker compose up -d mysql redis

log_info "等待 MySQL 就绪..."
sleep 10
attempt=0
while [ $attempt -lt 30 ]; do
    if docker compose exec -T mysql mysqladmin ping -u root -p"${MYSQL_ROOT_PASSWORD:-1145141919810}" --silent 2>/dev/null; then
        break
    fi
    attempt=$((attempt + 1))
    sleep 2
done

if [ $attempt -ge 30 ]; then
    log_error "MySQL 启动超时"
    docker compose logs mysql --tail=50
    exit 1
fi
log_success "MySQL 就绪"

log_info "启动 Analyzer..."
docker compose up -d analyzer

log_info "等待 Analyzer 就绪..."
if wait_for_service "http://localhost:8080/health" 30; then
    log_success "Analyzer 就绪"
else
    log_error "Analyzer 启动超时"
    docker compose logs analyzer --tail=50
    exit 1
fi

log_info "启动 Crawler..."
docker compose up -d crawler
sleep 5
log_success "Crawler 已启动"

# ============================================================
# 5. 导入测试数据
# ============================================================
log_section "5. 导入测试数据"

log_info "导入测试 IP..."
docker compose run --rm import -file /app/data/ips_grayscale.json

log_info "验证导入结果..."
IP_COUNT=$(curl -s http://localhost:8080/api/v1/ips | jq '.data | length' 2>/dev/null || echo "0")
if [ "$IP_COUNT" -ge 2 ]; then
    log_success "成功导入 $IP_COUNT 个 IP"
else
    log_error "IP 导入失败，只有 $IP_COUNT 个"
    exit 1
fi

# ============================================================
# 6. API 功能验证
# ============================================================
log_section "6. API 功能验证"

log_info "测试 /health..."
HEALTH=$(curl -s http://localhost:8080/health | jq -r '.status' 2>/dev/null)
if [ "$HEALTH" = "ok" ]; then
    log_success "/health 正常"
else
    log_error "/health 异常: $HEALTH"
fi

log_info "测试 /api/v1/ips..."
curl -s http://localhost:8080/api/v1/ips | jq '.data[] | {id, name, weight}' 2>/dev/null || echo "(jq 解析失败)"

log_info "测试 /api/v1/system/status..."
curl -s http://localhost:8080/api/v1/system/status | jq '.' 2>/dev/null || echo "(jq 解析失败)"

log_info "测试 /api/v1/system/scheduler..."
curl -s http://localhost:8080/api/v1/system/scheduler | jq '.data.active_ips' 2>/dev/null || echo "(jq 解析失败)"

# ============================================================
# 7. 触发抓取测试
# ============================================================
log_section "7. 触发抓取测试"

log_info "手动触发 IP 1 (初音ミク)..."
curl -s -X POST http://localhost:8080/api/v1/ips/1/trigger | jq '.' 2>/dev/null || echo "触发已发送"

log_info "手动触发 IP 2 (葬送のフリーレン)..."
curl -s -X POST http://localhost:8080/api/v1/ips/2/trigger | jq '.' 2>/dev/null || echo "触发已发送"

log_info "等待 Crawler 处理 (${WAIT_CRAWLER}秒)..."
log_info "可以在另一个终端查看日志: docker compose logs -f crawler"

# 进度条
for i in $(seq 1 $WAIT_CRAWLER); do
    printf "\r[%-50s] %d/%d 秒" $(printf '#%.0s' $(seq 1 $((i * 50 / WAIT_CRAWLER)))) $i $WAIT_CRAWLER
    sleep 1
done
echo ""

# ============================================================
# 8. 数据验证
# ============================================================
log_section "8. 数据验证"

log_info "检查 Redis 队列..."
PENDING=$(docker compose exec -T redis redis-cli LLEN animetop:tasks:pending 2>/dev/null || echo "0")
PROCESSING=$(docker compose exec -T redis redis-cli HLEN animetop:tasks:processing 2>/dev/null || echo "0")
log_info "待处理任务: $PENDING, 处理中: $PROCESSING"

log_info "检查 Redis 快照..."
SNAPSHOT_KEYS=$(docker compose exec -T redis redis-cli KEYS "animetop:snapshot:*" 2>/dev/null | wc -l || echo "0")
log_info "快照 Key 数量: $SNAPSHOT_KEYS"

log_info "检查 MySQL ip_stats_hourly..."
STATS_COUNT=$(docker compose exec -T mysql mysql -u animetop -p"${MYSQL_PASSWORD:-1145141919810}" animetop -N -e "SELECT COUNT(*) FROM ip_stats_hourly;" 2>/dev/null || echo "0")
if [ "$STATS_COUNT" -gt 0 ]; then
    log_success "ip_stats_hourly 有 $STATS_COUNT 条记录"
    docker compose exec -T mysql mysql -u animetop -p"${MYSQL_PASSWORD:-1145141919810}" animetop -e \
        "SELECT ip_id, hour_bucket, inflow, outflow, ROUND(liquidity_index, 2) as liquidity FROM ip_stats_hourly ORDER BY hour_bucket DESC LIMIT 5;" 2>/dev/null
else
    log_warn "ip_stats_hourly 暂无数据 (可能 Crawler 还在处理)"
fi

log_info "检查 MySQL item_snapshots..."
ITEMS_COUNT=$(docker compose exec -T mysql mysql -u animetop -p"${MYSQL_PASSWORD:-1145141919810}" animetop -N -e "SELECT COUNT(*) FROM item_snapshots;" 2>/dev/null || echo "0")
if [ "$ITEMS_COUNT" -gt 0 ]; then
    log_success "item_snapshots 有 $ITEMS_COUNT 条记录"
    docker compose exec -T mysql mysql -u animetop -p"${MYSQL_PASSWORD:-1145141919810}" animetop -e \
        "SELECT ip_id, status, COUNT(*) as count FROM item_snapshots GROUP BY ip_id, status;" 2>/dev/null
else
    log_warn "item_snapshots 暂无数据"
fi

# ============================================================
# 9. 检查服务状态
# ============================================================
log_section "9. 服务状态"

log_info "容器状态..."
docker compose ps

log_info "资源使用..."
docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}"

# ============================================================
# 10. 检查日志中的错误
# ============================================================
log_section "10. 错误检查"

log_info "检查 Analyzer 错误..."
ERROR_COUNT=$(docker compose logs analyzer 2>&1 | grep -i "error" | wc -l || echo "0")
if [ "$ERROR_COUNT" -gt 0 ]; then
    log_warn "Analyzer 日志中有 $ERROR_COUNT 个 error"
    docker compose logs analyzer 2>&1 | grep -i "error" | tail -5
else
    log_success "Analyzer 无错误"
fi

log_info "检查 Crawler 错误..."
ERROR_COUNT=$(docker compose logs crawler 2>&1 | grep -i "error" | wc -l || echo "0")
if [ "$ERROR_COUNT" -gt 0 ]; then
    log_warn "Crawler 日志中有 $ERROR_COUNT 个 error"
    docker compose logs crawler 2>&1 | grep -i "error" | tail -5
else
    log_success "Crawler 无错误"
fi

# ============================================================
# 总结
# ============================================================
log_section "测试总结"

echo ""
echo "测试完成！请检查以下内容:"
echo ""
echo "  1. API 端点是否正常响应"
echo "  2. MySQL 是否有统计数据"
echo "  3. Redis 是否有快照数据"
echo "  4. 日志中是否有异常错误"
echo ""
echo "常用命令:"
echo "  查看日志:     docker compose logs -f"
echo "  进入 MySQL:   make db-shell"
echo "  进入 Redis:   make redis-cli"
echo "  再次触发:     curl -X POST http://localhost:8080/api/v1/ips/1/trigger"
echo "  停止服务:     docker compose down"
echo "  清理数据:     docker compose down -v"
echo ""
