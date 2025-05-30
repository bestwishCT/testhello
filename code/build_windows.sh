#!/bin/bash
# 编译Windows版本的客户端

echo "开始编译Windows x64版本客户端..."

# 确保bin目录存在
mkdir -p bin

# 设置Windows交叉编译环境变量
export GOOS=windows
export GOARCH=amd64
export CGO_ENABLED=0

# 编译客户端
go build -o bin/client.exe ./cmd/client

# 检查编译结果
if [ $? -eq 0 ]; then
    echo "Windows客户端编译成功: bin/client.exe"
    # 显示文件信息
    ls -la bin/client.exe
else
    echo "编译失败！"
    exit 1
fi

echo "Windows客户端编译完成" 