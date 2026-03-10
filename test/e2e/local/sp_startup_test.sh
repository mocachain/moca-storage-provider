#!/bin/bash
# Storage Provider 启动测试脚本
# 用于验证 moca-storage-provider 可以正常启动

set -e

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
PROJECT_ROOT=$(cd "${SCRIPT_DIR}/../../.." && pwd)

cd "${PROJECT_ROOT}"

echo "=== 开始 Storage Provider 启动测试 ==="

# 检查 sp.json 文件是否存在
SP_JSON_PATH="${PROJECT_ROOT}/deployment/localup/sp.json"
if [ ! -f "$SP_JSON_PATH" ]; then
    echo "错误: SP 配置文件不存在: $SP_JSON_PATH"
    echo "请先运行 moca 节点的 node_startup_test.sh 生成 sp.json 文件"
    exit 1
fi
echo "✓ SP 配置文件存在: $SP_JSON_PATH"

# 1. 停止现有进程
echo "[1/5] 停止现有 Storage Provider 进程..."
bash ./deployment/localup/localup.sh stop || true
echo "✓ 停止完成"

# 2. 清理本地环境目录
echo "[2/5] 清理本地环境目录..."
rm -fr deployment/localup/local_env
echo "✓ 清理完成"

# 3. 生成配置
echo "[3/5] 生成 Storage Provider 配置..."
echo "使用配置: $SP_JSON_PATH"
echo "数据库配置: root moca 127.0.0.1:3306"
bash ./deployment/localup/localup.sh generate "$SP_JSON_PATH" root moca 127.0.0.1:3306

if [ $? -ne 0 ]; then
    echo "错误: 配置生成失败"
    exit 1
fi
echo "✓ 配置生成成功"

# 4. 重置环境
echo "[4/5] 重置 Storage Provider 环境..."
bash ./deployment/localup/localup.sh reset

if [ $? -ne 0 ]; then
    echo "错误: 环境重置失败"
    exit 1
fi
echo "✓ 环境重置成功"

# 5. 启动 Storage Provider
echo "[5/5] 启动 Storage Provider..."
bash ./deployment/localup/localup.sh start

if [ $? -ne 0 ]; then
    echo "错误: Storage Provider 启动失败"
    exit 1
fi

# 等待进程启动
sleep 5

# 验证进程是否运行
if ! pgrep -f "moca-sp" > /dev/null; then
    echo "错误: Storage Provider 进程未运行"
    exit 1
fi

echo "✓ Storage Provider 进程运行正常"

# 检查启动的 SP 数量
SP_COUNT=$(pgrep -f "moca-sp" | wc -l)
echo "✓ 成功启动 $SP_COUNT 个 Storage Provider 进程"

echo ""
echo "=== Storage Provider 启动测试完成 ==="
echo "✓ 所有测试步骤通过"

