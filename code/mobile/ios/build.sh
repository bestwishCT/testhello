#!/bin/bash

# 设置错误时退出
set -e

# 强制使用 Go 1.23.8
export PATH="/opt/hostedtoolcache/go/1.23.8/x64/bin:$PATH"

# 打印 Go 版本
go version

# 设置 GOPATH 和 GOPROXY
export GOPATH="$HOME/go"
export PATH="$GOPATH/bin:$PATH"
export GOPROXY=direct

# 创建临时目录
TEMP_DIR=$(mktemp -d)
cd $TEMP_DIR

# 创建临时的 go.mod
cat > go.mod << EOF
module temp

go 1.23

require (
    golang.org/x/mobile v0.0.0-20250520180527-a1d90793fc63
    golang.org/x/tools v0.33.0
    golang.org/x/mod v0.24.0
    golang.org/x/sync v0.14.0
)
EOF

# 安装最新版本的 gomobile 和依赖
go mod tidy
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest

# 返回原目录
cd -

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

# 清理临时目录
rm -rf $TEMP_DIR 