#!/bin/bash

echo "===== 构建ShiledP2P系统 ====="
go build -o bin/master cmd/master/main.go
go build -o bin/agent cmd/agent/main.go
go build -o bin/client cmd/client/main.go

echo "===== 清除日志文件 ====="
> master.log
> agent.log
> client.log

echo "===== 启动Master节点 ====="
./bin/master > master.log 2>&1 &
MASTER_PID=$!
echo "Master节点已启动，PID: $MASTER_PID"

# 等待Master启动完成
sleep 5

echo "===== 启动Agent节点 ====="
./bin/agent > agent.log 2>&1 &
AGENT_PID=$!
echo "Agent节点已启动，PID: $AGENT_PID"

# 等待Agent连接到Master并注册
sleep 10

echo "===== 启动Client节点 ====="
./bin/client > client.log 2>&1 &
CLIENT_PID=$!
echo "Client节点已启动，PID: $CLIENT_PID"

# 等待Client连接到Master和Agent
sleep 15

echo "===== 测试代理连接 ====="
echo "尝试通过代理访问百度..."
curl -v -x http://localhost:58080 http://www.baidu.com

echo "===== 检查各节点日志 ====="
echo "查看Master日志中的Agent注册信息:"
grep -A 5 "Agent 已成功注册" master.log

echo "查看Client日志中的Agent连接信息:"
grep -A 5 "成功连接到Agent" client.log

echo "查看Agent日志中的代理请求信息:"
grep -A 10 "收到代理请求" agent.log

echo "===== 清理进程 ====="
echo "按Ctrl+C停止测试并清理进程..."
read -r

kill $MASTER_PID $AGENT_PID $CLIENT_PID
echo "所有进程已停止" 