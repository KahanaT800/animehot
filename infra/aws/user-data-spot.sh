#!/bin/bash
# =============================================================================
# Spot Instance User Data Script
# K3s Agent + Tailscale + Spot 中断处理
#
# 缩容策略:
# - 空闲缩容: KEDA 检测队列空 → replicas=0 → spot-asg-scaler 缩容 ASG
# - Ban 自杀: py-crawler 检测封禁 → 终止实例 → ASG 自动补充
# - Spot 中断: AWS 提前通知 → drain + cleanup → 实例终止
# =============================================================================
set -euo pipefail

# =====================================================
# 配置变量 (通过 CloudFormation 注入)
# =====================================================
K3S_URL="${K3S_URL:-https://100.99.127.100:6443}"
TAILSCALE_AUTHKEY_PARAM="${TAILSCALE_AUTHKEY_PARAM:-/animehot/tailscale-authkey}"
K3S_TOKEN_PARAM="${K3S_TOKEN_PARAM:-/animehot/k3s-token}"
AWS_REGION="${AWS_REGION:-ap-northeast-1}"

# =====================================================
# 获取实例元数据 (IMDSv2)
# =====================================================
TOKEN=$(curl -sf -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")
INSTANCE_ID=$(curl -sf -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/instance-id)
AVAILABILITY_ZONE=$(curl -sf -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/placement/availability-zone)
HOSTNAME="spot-${INSTANCE_ID}"

echo "=== Spot Instance Bootstrap ==="
echo "Instance ID: $INSTANCE_ID"
echo "Availability Zone: $AVAILABILITY_ZONE"
echo "Hostname: $HOSTNAME"

# =====================================================
# 1. 安装 Tailscale
# =====================================================
echo "=== Installing Tailscale ==="
curl -fsSL https://tailscale.com/install.sh | sh

# 从 SSM 获取 Tailscale Auth Key
TAILSCALE_AUTHKEY=$(aws ssm get-parameter \
  --name "$TAILSCALE_AUTHKEY_PARAM" \
  --with-decryption \
  --region "$AWS_REGION" \
  --query 'Parameter.Value' \
  --output text)

# 启动 Tailscale
tailscale up \
  --authkey="$TAILSCALE_AUTHKEY" \
  --hostname="$HOSTNAME" \
  --accept-routes

# 等待 Tailscale 连接
sleep 10
TAILSCALE_IP=$(tailscale ip -4)
echo "Tailscale IP: $TAILSCALE_IP"

# =====================================================
# 2. 安装 K3s Agent
# =====================================================
echo "=== Installing K3s Agent ==="

# 从 SSM 获取 K3s Token
K3S_TOKEN=$(aws ssm get-parameter \
  --name "$K3S_TOKEN_PARAM" \
  --with-decryption \
  --region "$AWS_REGION" \
  --query 'Parameter.Value' \
  --output text)

# 配置 Flannel MTU (匹配 Tailscale MTU 1280)
mkdir -p /etc/rancher/k3s
cat > /etc/rancher/k3s/config.yaml <<EOF
# K3s Agent 配置
flannel-iface: tailscale0
node-label:
  - "node-role=spot"
  - "topology.kubernetes.io/zone=$AVAILABILITY_ZONE"
EOF

# 安装 K3s Agent
curl -sfL https://get.k3s.io | \
  K3S_URL="$K3S_URL" \
  K3S_TOKEN="$K3S_TOKEN" \
  sh -s - agent

# 等待 K3s Agent 启动
sleep 30
echo "K3s Agent started"

# =====================================================
# 3. Tailscale 清理 (Shutdown Hook)
# =====================================================
# 在实例终止时自动注销 Tailscale，避免残留设备
echo "=== Setting up Tailscale cleanup hook ==="

cat > /etc/systemd/system/tailscale-cleanup.service <<'CLEANUP_EOF'
[Unit]
Description=Tailscale cleanup on shutdown
DefaultDependencies=no
Before=shutdown.target reboot.target halt.target
After=network.target tailscaled.service

[Service]
Type=oneshot
ExecStart=/usr/bin/tailscale logout
TimeoutStartSec=30
RemainAfterExit=yes

[Install]
WantedBy=halt.target reboot.target shutdown.target
CLEANUP_EOF

systemctl daemon-reload
systemctl enable tailscale-cleanup.service

echo "Tailscale cleanup hook installed"

# =====================================================
# 4. Spot 中断处理
# =====================================================
# 注意: Agent 节点无法直接使用 kubectl
# Pod 优雅关闭由 kubelet 处理 (发送 SIGTERM)
# 节点清理由 spot-asg-scaler 处理 (场景E)
echo "=== Setting up Spot interruption handler ==="

(
  while true; do
    # 检查 Spot 中断通知 (AWS 提前 2 分钟警告)
    IMDS_TOKEN=$(curl -sf -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60" || true)
    if [ -n "$IMDS_TOKEN" ] && curl -sf -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/spot/instance-action >/dev/null 2>&1; then
      echo "=== Spot interruption notice received! ==="
      # tailscale-cleanup.service 会在 shutdown 时自动执行
      echo "Spot interruption detected, system will shutdown"
      exit 0
    fi
    sleep 5
  done
) &

echo "Spot interruption handler started"

# =====================================================
# 5. 完成
# =====================================================
echo "=== Spot Instance Bootstrap Complete ==="
echo "K3s Agent running on $HOSTNAME"
echo "Tailscale IP: $TAILSCALE_IP"
echo "Node lifecycle: KEDA controls scaling, ASG handles termination"
