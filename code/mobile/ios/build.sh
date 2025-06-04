#!/bin/bash

# 设置错误时退出
set -e

# 强制使用 Go 1.20.14
export PATH="/opt/hostedtoolcache/go/1.20.14/x64/bin:$PATH"

# 打印 Go 版本
go version

# 设置 GOPATH 和 GOPROXY
export GOPATH="$HOME/go"
export PATH="$GOPATH/bin:$PATH"
export GOPROXY=direct

# 安装与 Go 1.20 兼容的 gomobile 版本
go get golang.org/x/mobile/cmd/gomobile@v0.0.0-20230301163155-e0f57694e12c
go get golang.org/x/mobile/cmd/gobind@v0.0.0-20230301163155-e0f57694e12c

# 初始化 gomobile
gomobile init

# 清理旧的 .xcframework 文件
rm -rf *.xcframework

# 构建 iOS 框架
gomobile bind -target=ios -iosversion=12.0 -prefix=Shiledp2p -o Shiledp2p.xcframework github.com/shiledp2p/mobile

# 检查构建结果
if [ $? -eq 0 ]; then
    echo ""
    echo "iOS framework build successful!"
    echo "Output: Shiledp2p.xcframework"
    
    # 检查文件大小
    FRAMEWORK_SIZE=$(du -sh Shiledp2p.xcframework | cut -f1)
    echo "Framework size: $FRAMEWORK_SIZE"
    
    # 列出框架内容
    echo "Framework contents:"
    ls -la Shiledp2p.xcframework
else
    echo ""
    echo "Build failed, please check error messages"
    exit 1
fi 