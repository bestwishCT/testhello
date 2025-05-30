#!/bin/bash
# 编译macOS Intel版本的客户端

echo "开始编译macOS Intel版本客户端..."

# 确保bin目录存在
mkdir -p bin

# 设置macOS Intel交叉编译环境变量
export GOOS=darwin
export GOARCH=amd64
export CGO_ENABLED=0

# 编译客户端
go build -o bin/client_macos_intel ./cmd/client

# 检查编译结果
if [ $? -eq 0 ]; then
    echo "macOS Intel客户端编译成功: bin/client_macos_intel"
    # 显示文件信息
    ls -la bin/client_macos_intel
else
    echo "编译失败！"
    exit 1
fi

echo "macOS Intel客户端编译完成" 