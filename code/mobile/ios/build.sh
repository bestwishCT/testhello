#!/bin/bash

# 设置环境变量
export GO111MODULE=on
export CGO_ENABLED=1
export GOOS=ios
export GOARCH=arm64

# 清理旧的构建文件
if [ -d "*.framework" ]; then
    echo "Cleaning old framework files..."
    rm -rf *.framework
fi

# 设置构建参数
BUILD_TAGS="ios"
BUILD_LDFLAGS="-s -w"
BUILD_DEBUG="false"

# 使用 gomobile 构建 iOS 框架
echo "Building iOS framework..."
cd ../..
gomobile bind -target=ios \
    -o ios/ShileP2P.framework \
    -prefix=ShileP2P \
    -tags=$BUILD_TAGS \
    -ldflags="$BUILD_LDFLAGS" \
    -v=$BUILD_DEBUG \
    ./mobile

# 检查构建结果
if [ $? -eq 0 ]; then
    echo ""
    echo "iOS framework build successful!"
    echo "Output: ios/ShileP2P.framework"
    
    # 检查文件大小
    FRAMEWORK_SIZE=$(du -sh ios/ShileP2P.framework | cut -f1)
    echo "Framework size: $FRAMEWORK_SIZE"
else
    echo ""
    echo "Build failed, please check error messages"
    exit 1
fi

# 清理临时文件
if [ -f "*.a" ]; then
    rm -f *.a
fi 