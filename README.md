# KCP 10Gbps高性能测试工具

这个测试工具用于测试使用KCP协议的libp2p在10Gbps高速网络环境下的性能表现。

## 编译

```bash
cd 10g_test
go mod tidy
go build -o kcp_10g kcp_10g.go
```

## 使用方法

### 服务端模式

```bash
./kcp_10g -server -port 12345
```

### 客户端模式 - 吞吐量测试

```bash
./kcp_10g -target /ip4/SERVER_IP/udp/12345/kcp/enc/aes/key/0123456789ABCDEF/p2p/SERVER_ID -size 65536 -parallel 32 -duration 60s
```

### 客户端模式 - 延迟测试

```bash
./kcp_10g -target /ip4/SERVER_IP/udp/12345/kcp/enc/aes/key/0123456789ABCDEF/p2p/SERVER_ID -latency -size 1024 -duration 30s
```

## KCP参数说明

可以通过以下参数调整KCP设置，以适应不同网络环境：

- `-sndwnd`: 发送窗口大小，默认65535
- `-rcvwnd`: 接收窗口大小，默认65535
- `-interval`: 内部更新时钟间隔(ms)，默认1ms
- `-nodelay`: 是否启用NoDelay模式，默认1 (开启)
- `-resend`: 快速重传模式，默认2
- `-nocong`: 是否关闭拥塞控制，默认1 (关闭)
- `-mtu`: 最大传输单元，默认1400

## 代码修改说明

最新的修改解决了libp2p传输层接口兼容性问题，通过将KCP传输层用fx.Provide包装后再添加到Config.Transports列表。 