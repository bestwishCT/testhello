#!/bin/bash

# 设置错误时退出
set -e

# 设置环境变量
export GO111MODULE=on
export CGO_ENABLED=1
export GOOS=ios
export GOARCH=arm64

echo "Current directory: $(pwd)"
echo "GO version: $(go version)"
echo "GOPATH: $GOPATH"
echo "GO111MODULE: $GO111MODULE"
echo "CGO_ENABLED: $CGO_ENABLED"

# 清理旧的构建文件
if [ -d "*.framework" ]; then
    echo "Cleaning old framework files..."
    rm -rf *.framework
fi
if [ -d "*.xcframework" ]; then
    echo "Cleaning old xcframework files..."
    rm -rf *.xcframework
fi

# 设置构建参数
BUILD_TAGS="ios"
BUILD_LDFLAGS="-s -w"
BUILD_DEBUG="true"  # 启用详细输出

# 使用 gomobile 构建 iOS 框架
echo "Building iOS framework..."
cd ../..
echo "Changed to directory: $(pwd)"

# 确保 gomobile 已安装
if ! command -v gomobile &> /dev/null; then
    echo "gomobile not found, installing..."
    go install golang.org/x/mobile/cmd/gomobile@latest
    go install golang.org/x/mobile/cmd/gobind@latest
    gomobile init
fi

# 构建框架
gomobile bind -target=ios \
    -o mobile/ios/ShileP2P.xcframework \
    -prefix=ShileP2P \
    -tags=$BUILD_TAGS \
    -ldflags="$BUILD_LDFLAGS" \
    -v=$BUILD_DEBUG \
    ./mobile/ios

# 检查构建结果
if [ $? -eq 0 ]; then
    echo ""
    echo "iOS framework build successful!"
    echo "Output: mobile/ios/ShileP2P.xcframework"
    
    # 检查文件大小
    FRAMEWORK_SIZE=$(du -sh mobile/ios/ShileP2P.xcframework | cut -f1)
    echo "Framework size: $FRAMEWORK_SIZE"
    
    # 列出框架内容
    echo "Framework contents:"
    ls -la mobile/ios/ShileP2P.xcframework
else
    echo ""
    echo "Build failed, please check error messages"
    exit 1
fi

# 清理临时文件
if [ -f "*.a" ]; then
    rm -f *.a
fi 