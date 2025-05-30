# ShiledP2P Android客户端使用说明

## 功能概述

ShiledP2P是一个基于libp2p的P2P通信库，提供了以下核心功能：

1. 节点发现与连接
2. 网络测速
3. 状态监控
4. 自动重连
5. 多协议支持（QUIC和KCP）

## 主要接口说明

### 1. 初始化客户端

```java
// 创建客户端实例
Shiledp2p client = new Shiledp2p(listenQuicAddr, listenKcpAddr);

// 设置回调
client.setCallback(new Shiledp2p.Callback() {
    @Override
    public void onStatusChange(String status) {
        // 处理状态变化
    }

    @Override
    public void onError(String error) {
        // 处理错误
    }

    @Override
    public void onAgentList(String[] agents) {
        // 处理Agent列表更新
    }

    @Override
    public void onSpeedTest(String result) {
        // 处理测速结果
    }
});
```

### 2. 启动客户端

```java
// apiPort: API代理端口
// tunnelPort: 隧道代理端口
client.start(apiPort, tunnelPort);
```

### 3. 停止客户端

```java
client.stop();
```

### 4. 连接对等节点

```java
// peerID: 目标节点的ID
client.connectToPeer(peerID);
```

### 5. 获取已连接的Agent列表

```java
String[] agents = client.getConnectedAgents();
```

### 6. 运行测速

```java
// targetPeerID: 目标节点ID
// fileSize: 测试文件大小（字节）
// duration: 测试持续时间（秒）
client.runSpeedTest(targetPeerID, fileSize, duration);
```

### 7. 重新从Master获取Agent列表

```java
client.bootstrap();
```

## 回调说明

### 1. onStatusChange
- 用途：通知状态变化
- 触发时机：
  - 客户端启动/停止
  - 新节点连接/断开
  - 测速开始
  - 其他状态变化

### 2. onError
- 用途：通知错误信息
- 触发时机：
  - 连接失败
  - 测速失败
  - 其他错误情况

### 3. onAgentList
- 用途：通知Agent列表更新
- 触发时机：从Master获取到新的Agent列表

### 4. onSpeedTest
- 用途：通知测速结果
- 触发时机：测速完成
- 结果格式：
```
测试ID: xxx
总传输数据: xx.xx MB
测试时长: xx.xx 秒
吞吐量: xx.xx MB/s
网络速率: xx.xx Mbps
```

## 注意事项

1. 所有P2P操作建议在后台线程中执行
2. 确保在使用前已获取必要权限
3. 注意处理网络状态变化
4. 在应用退出时正确关闭P2P连接
5. 测速操作可能会消耗较多带宽，建议在合适的时机进行
6. 定期检查连接状态，必要时重新连接

## 常见问题

1. 连接失败
   - 检查网络权限
   - 确认目标节点是否在线
   - 检查防火墙设置

2. 测速失败
   - 确保与目标节点已建立连接
   - 检查网络状态
   - 确认参数设置合理

3. 无法获取Agent列表
   - 检查与Master的连接
   - 确认网络状态
   - 尝试重新调用bootstrap() 