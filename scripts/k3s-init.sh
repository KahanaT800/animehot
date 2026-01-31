#!/bin/bash
# =============================================================================
# K3s Server Initialization Script
# 在主 EC2 上运行此脚本初始化 K3s Server
# =============================================================================
set -euo pipefail

# =====================================================
# 配置变量
# =====================================================
AWS_REGION="${AWS_REGION:-ap-northeast-1}"
K3S_TOKEN_PARAM="${K3S_TOKEN_PARAM:-/animehot/k3s-token}"
TAILSCALE_AUTHKEY_PARAM="${TAILSCALE_AUTHKEY_PARAM:-/animehot/tailscale-authkey}"

echo "=============================================="
echo "  K3s Server Initialization Script"
echo "=============================================="

# =====================================================
# 1. 安装 Tailscale
# =====================================================
echo ""
echo "=== Step 1: Installing Tailscale ==="

if ! command -v tailscale &> /dev/null; then
    curl -fsSL https://tailscale.com/install.sh | sh
    echo "Tailscale installed"
else
    echo "Tailscale already installed"
fi

# 检查 Tailscale 状态
if ! tailscale status &> /dev/null; then
    echo "Starting Tailscale..."
    # 如果有 authkey，使用它；否则提示手动认证
    if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then
        tailscale up --authkey="$TAILSCALE_AUTHKEY" --hostname=k3s-master
    else
        echo "Please authenticate Tailscale manually:"
        tailscale up --hostname=k3s-master
    fi
fi

# 等待 Tailscale 连接
sleep 5
TAILSCALE_IP=$(tailscale ip -4)
echo "Tailscale IP: $TAILSCALE_IP"

# =====================================================
# 2. 安装 K3s Server
# =====================================================
echo ""
echo "=== Step 2: Installing K3s Server ==="

if ! command -v k3s &> /dev/null; then
    curl -sfL https://get.k3s.io | sh -s - \
        --write-kubeconfig-mode 644 \
        --disable traefik \
        --node-label node-role=master \
        --tls-san "$TAILSCALE_IP" \
        --flannel-iface tailscale0 \
        --flannel-backend vxlan
    echo "K3s Server installed"
else
    echo "K3s already installed"
fi

# 等待 K3s 启动
echo "Waiting for K3s to be ready..."
sleep 30
kubectl wait --for=condition=Ready node/$(hostname) --timeout=120s
echo "K3s Server is ready"

# =====================================================
# 3. Taint Master 节点
# =====================================================
echo ""
echo "=== Step 3: Tainting master node ==="

kubectl taint nodes $(hostname) node-role=master:NoSchedule --overwrite || true
echo "Master node tainted"

# =====================================================
# 4. 保存 K3s Token 到 SSM
# =====================================================
echo ""
echo "=== Step 4: Saving K3s token to SSM ==="

K3S_TOKEN=$(cat /var/lib/rancher/k3s/server/node-token)

# 检查 AWS CLI
if command -v aws &> /dev/null; then
    aws ssm put-parameter \
        --name "$K3S_TOKEN_PARAM" \
        --value "$K3S_TOKEN" \
        --type SecureString \
        --overwrite \
        --region "$AWS_REGION"
    echo "K3s token saved to SSM: $K3S_TOKEN_PARAM"
else
    echo "WARNING: AWS CLI not found. Please manually save the token:"
    echo "Token: $K3S_TOKEN"
fi

# =====================================================
# 5. 创建数据目录
# =====================================================
echo ""
echo "=== Step 5: Creating data directories ==="

mkdir -p /var/lib/animehot/redis
chown -R 1000:1000 /var/lib/animehot
echo "Data directories created"

# =====================================================
# 6. 安装 Helm
# =====================================================
echo ""
echo "=== Step 6: Installing Helm ==="

if ! command -v helm &> /dev/null; then
    curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
    echo "Helm installed"
else
    echo "Helm already installed"
fi

# =====================================================
# 7. 安装 KEDA
# =====================================================
echo ""
echo "=== Step 7: Installing KEDA ==="

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

# 添加 KEDA Helm repo
helm repo add kedacore https://kedacore.github.io/charts
helm repo update

# 安装 KEDA
if ! kubectl get namespace keda &> /dev/null; then
    helm install keda kedacore/keda \
        --namespace keda \
        --create-namespace \
        --wait
    echo "KEDA installed"
else
    echo "KEDA already installed"
fi

# =====================================================
# 8. 验证安装
# =====================================================
echo ""
echo "=== Step 8: Verifying installation ==="

echo "Nodes:"
kubectl get nodes -o wide

echo ""
echo "KEDA pods:"
kubectl get pods -n keda

echo ""
echo "=============================================="
echo "  K3s Server Initialization Complete!"
echo "=============================================="
echo ""
echo "Next steps:"
echo "1. Apply K8s manifests: kubectl apply -f k8s/"
echo "2. Create AWS ASG: aws cloudformation deploy --template-file infra/aws/asg.yaml ..."
echo ""
echo "K3s Server URL: https://$TAILSCALE_IP:6443"
echo "Token saved to: $K3S_TOKEN_PARAM"
