# KCP 传输层 (KCP Transport)

KCP传输层是libp2p网络协议栈的一个可选传输层实现，它基于KCP协议提供可靠的UDP传输功能。

## 特性

- 基于KCP实现的可靠UDP传输
- 内置多种加密方式，不依赖TLS
- 与libp2p的现有传输层无缝集成
- 对NAT穿透场景有较好的支持

## 安装

首先，确保您已经安装了kcp-go依赖：

```bash
go get github.com/xtaci/kcp-go
```

然后，将本传输层添加到您的libp2p节点中。

## 使用方法

### 基本用法

下面是一个简单的使用KCP传输层的例子：

```go
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/p2p/transport/kcp"
	
	ma "github.com/multiformats/go-multiaddr"
)

func main() {
	// 生成主机密钥
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, rand.Reader)
	if err != nil {
		panic(err)
	}
	
	// 创建一个监听地址，使用KCP传输层
	// KCP地址格式：/ip4/0.0.0.0/udp/0/kcp/aes/key/{加密密钥}
	addr, _ := ma.NewMultiaddr("/ip4/0.0.0.0/udp/0/kcp/aes/key/mySecretKey123456")
	
	// 创建libp2p主机
	host, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(addr),
		libp2p.Transport(kcp.NewTransport),
	)
	if err != nil {
		panic(err)
	}
	
	// 打印主机地址
	fmt.Println("Host ID:", host.ID())
	fmt.Println("Host Address:", host.Addrs())
	
	// 保持主机运行
	select {}
}
```

### 多地址格式

KCP传输层使用以下格式的多地址：

```
/ip4/{ip}/udp/{port}/kcp/{加密方式}/key/{加密密钥}
```

加密方式可以是以下之一：
- `none`: 无加密
- `aes`: AES加密
- `tea`: TEA加密
- `xtea`: XTEA加密
- `salsa20`: Salsa20加密
- `blowfish`: Blowfish加密
- `twofish`: Twofish加密
- `cast5`: CAST5加密
- `3des`: 三重DES加密
- `sm4`: SM4加密
- `xor`: 简单XOR加密

## 安全性说明

本传输层与其他libp2p传输层的主要区别是：

1. 使用KCP自身的加密机制，而不是额外的TLS加密
2. 由于没有TLS证书交换，节点身份验证简化为使用KCP密钥作为标识的一部分
3. 适合于在信任的网络环境中使用，或搭配其他身份验证机制使用

## 与其他传输层集成

KCP传输层可以与其他libp2p传输层（如TCP、QUIC等）一起使用，以提供多种连接选项：

```go
host, err := libp2p.New(
    libp2p.Identity(priv),
    libp2p.ListenAddrs(
        tcpAddr,  // TCP地址
        quicAddr, // QUIC地址
        kcpAddr,  // KCP地址
    ),
    libp2p.Transport(tcp.NewTCPTransport),
    libp2p.Transport(quic.NewTransport),
    libp2p.Transport(kcp.NewTransport),
)
```

## 注意事项

- KCP传输层的安全性完全依赖于KCP自身的加密机制，因此选择强密钥非常重要
- 在实际使用中，应当选择合适的密钥长度和加密算法，以确保足够的安全性
- 为了获得最佳性能，可能需要根据网络条件调整KCP参数

## 依赖

- github.com/xtaci/kcp-go
- github.com/libp2p/go-libp2p

## 贡献

欢迎提交问题报告、功能请求和代码贡献！ 