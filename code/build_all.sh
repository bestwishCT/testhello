#!/bin/bash
# 一次性编译所有平台的客户端

echo "======= 开始编译所有平台的客户端 ======="
echo ""

# 确保bin目录存在
mkdir -p bin

# 编译Linux AMD64版本
echo "编译Linux AMD64版本..."
export GOOS=linux
export GOARCH=amd64
export CGO_ENABLED=0
go build -o bin/client_linux_amd64 ./cmd/client
echo "Linux AMD64版本完成"
echo ""

# 编译Windows AMD64版本
echo "编译Windows AMD64版本..."
export GOOS=windows
export GOARCH=amd64
export CGO_ENABLED=0
go build -o bin/client.exe ./cmd/client
echo "Windows AMD64版本完成"
echo ""

# 编译macOS Intel版本
echo "编译macOS Intel版本..."
export GOOS=darwin
export GOARCH=amd64
export CGO_ENABLED=0
go build -o bin/client_macos_intel ./cmd/client
echo "macOS Intel版本完成"
echo ""

# 编译macOS ARM版本 (M1/M2)
echo "编译macOS ARM版本..."
export GOOS=darwin
export GOARCH=arm64
export CGO_ENABLED=0
go build -o bin/client_macos_arm ./cmd/client
echo "macOS ARM版本完成"
echo ""

# 显示所有编译结果
echo "======= 编译结果 ======="
ls -la bin/client*

echo ""
echo "所有客户端编译完成" 