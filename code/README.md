# ShiledP2P

ShiledP2P是一个基于P2P技术的数据转发网络，利用libp2p提供的强大功能实现节点间的发现、连接、数据传输和NAT穿透。

## 系统架构

系统包含三个核心组件：

1. **Master (入口节点)**: 单点入口/引导服务器
2. **Agent (主工作节点)**: 分布式工作节点，连接Master，并接受Client连接
3. **Client (客户端节点)**: 终端用户节点，通过Master连接Agent，进而连接其他Client

## 功能特点

- Master节点提供引导服务，帮助Client发现Agent节点
- Agent节点负责维护Client连接、转发数据、协助Client间发现与直连
- Client节点可以通过Agent发现其他Client并尝试建立直接连接
- 支持NAT穿透功能，帮助Client之间建立直接P2P连接
- 提供HTTP/HTTPS代理功能，允许将本地流量通过P2P网络转发出去

## 技术选型

- 基于go-libp2p (v0.41.1)实现P2P网络功能
- 使用QUIC协议作为主要传输协议
- 利用DNS TXT记录发现Master节点

## 编译与运行

### 前提条件

- Go 1.21或更高版本
- 设置好DNS解析（test.halochat.dev的TXT记录应包含Master节点的PeerID）

### 编译

```bash
# 编译所有组件
go build -o bin/master ./cmd/master
go build -o bin/agent ./cmd/agent
go build -o bin/client ./cmd/client
```

### 运行Master节点

```bash
./bin/master --listen "/ip4/0.0.0.0/udp/8440/quic"
```

### 运行Agent节点

```bash
./bin/agent --quic "/ip4/0.0.0.0/udp/0/quic"
```

### 运行Client节点

```bash
./bin/client --listen "/ip4/0.0.0.0/udp/0/quic" --proxy 58080
```

## 使用方法

1. 启动Master节点，确保其PeerID已通过TXT记录发布
2. 启动一个或多个Agent节点，它们会自动连接到Master
3. 启动Client节点，它们会先连接Master获取Agent信息，然后连接到Agent
4. Client会在本地启动一个代理服务器（默认58080端口），可以将HTTP/HTTPS流量转发出去
5. 多个Client之间会通过Agent协调进行NAT穿透，尝试建立直接P2P连接

## 开发说明

本项目仍处于开发阶段，以下特性将在未来版本中添加：

- 完善的节点身份验证和通道加密
- 负载均衡和更智能的Agent选择算法
- 可配置的私钥存储，确保节点重启后保持相同的PeerID
- 更完善的错误处理和日志记录

## 协议及路由

- `/shiledp2p/v1/discovery`: 节点发现协议
- `/shiledp2p/v1/proxy`: 代理协议
- `/shiledp2p/v1/holepunch`: NAT穿透协议
- `/shiledp2p/v1/heartbeat`: 心跳协议 

## 部署
```bash
sudo nohup ./master --listen "/ip4/0.0.0.0/udp/8440/quic-v1"  > master.log 2>&1 &

sudo nohup  ./agent --quic "/ip4/0.0.0.0/udp/8441/quic-v1" --master-ip "82.156.32.79" > agent.log 2>&1 &
```