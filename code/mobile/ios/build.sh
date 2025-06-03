#!/bin/bash

# 设置错误时退出
set -e

# 强制使用 actions/setup-go 安装的 Go 版本
export PATH=$(go env GOROOT)/bin:$PATH
echo "which go: $(which go)"
go version

# 设置环境变量
export GO111MODULE=on
export CGO_ENABLED=1

echo "Current directory: $(pwd)"
echo "GO version: $(go version)"
echo "GOPATH: $GOPATH"
echo "GO111MODULE: $GO111MODULE"
echo "CGO_ENABLED: $CGO_ENABLED"

# 清理旧的构建文件
if [ -d "*.xcframework" ]; then
    echo "Cleaning old xcframework files..."
    rm -rf *.xcframework
fi

# 使用 gomobile 构建 iOS 框架
echo "Building iOS framework..."
cd ../..
echo "Changed to directory: $(pwd)"

# 设置 GOPATH
export GOPATH=$(go env GOPATH)
export PATH=$PATH:$GOPATH/bin

# 确保 gomobile 已安装
echo "Installing gomobile..."
go install golang.org/x/mobile/cmd/gomobile@v0.0.0-20240213143359-d1f7d3436075
go install golang.org/x/mobile/cmd/gobind@v0.0.0-20240213143359-d1f7d3436075

# 初始化 gomobile
echo "Initializing gomobile..."
gomobile init

# 下载依赖
echo "Downloading dependencies..."
go mod tidy

# 构建框架
echo "Building framework..."
gomobile bind -target=ios \
    -o mobile/ios/SimpleDemo.xcframework \
    -prefix=SimpleDemo \
    shiledp2p/mobile/ios

# 检查构建结果
if [ $? -eq 0 ]; then
    echo ""
    echo "iOS framework build successful!"
    echo "Output: mobile/ios/SimpleDemo.xcframework"
    
    # 检查文件大小
    FRAMEWORK_SIZE=$(du -sh mobile/ios/SimpleDemo.xcframework | cut -f1)
    echo "Framework size: $FRAMEWORK_SIZE"
    
    # 列出框架内容
    echo "Framework contents:"
    ls -la mobile/ios/SimpleDemo.xcframework
else
    echo ""
    echo "Build failed, please check error messages"
    exit 1
fi 