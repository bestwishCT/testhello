package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	kcptransport "github.com/libp2p/go-libp2p/p2p/transport/kcp"

	ma "github.com/multiformats/go-multiaddr"
)

func main() {
	// 解析命令行参数
	if len(os.Args) != 1 && len(os.Args) != 2 {
		fmt.Println("使用方法: ./basic [<peer-multiaddr>]")
		fmt.Println("  不带参数: 运行为监听模式")
		fmt.Println("  带一个参数: 运行为拨号模式，连接指定的对等节点")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 生成主机密钥
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, rand.Reader)
	if err != nil {
		panic(err)
	}

	// 创建一个监听地址，使用KCP传输层
	// KCP地址格式：/ip4/0.0.0.0/udp/8765/kcp/aes/key/mySecretKey123456
	// 注意：此处的key会用于KCP的加密，而不需要额外的TLS加密
	addr, _ := ma.NewMultiaddr("/ip4/0.0.0.0/udp/8765/kcp/aes/key/mySecretKey123456")

	// 创建libp2p主机
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(addr),
		libp2p.Transport(kcptransport.NewTransport),
	)
	if err != nil {
		panic(err)
	}

	// 打印主机地址
	fmt.Printf("主机启动，ID: %s\n", h.ID())
	fmt.Printf("主机地址: %s\n", h.Addrs())

	// 如果提供了目标对等节点地址，尝试连接
	if len(os.Args) == 2 {
		targetAddrStr := os.Args[1]
		targetAddr, err := ma.NewMultiaddr(targetAddrStr)
		if err != nil {
			log.Fatalf("无效的目标地址: %v", err)
		}

		// 从多地址中提取对等节点信息
		peerInfo, err := peer.AddrInfoFromP2pAddr(targetAddr)
		if err != nil {
			log.Fatalf("提取对等节点信息失败: %v", err)
		}

		// 添加到对等节点存储
		h.Peerstore().AddAddrs(peerInfo.ID, peerInfo.Addrs, peerstore.PermanentAddrTTL)

		// 连接到对等节点
		fmt.Printf("正在连接到 %s\n", peerInfo.ID)
		err = h.Connect(ctx, *peerInfo)
		if err != nil {
			log.Fatalf("连接失败: %v", err)
		}

		fmt.Printf("成功连接到 %s\n", peerInfo.ID)

		// 打开一个流，并发送数据
		s, err := h.NewStream(ctx, peerInfo.ID, "/kcp-demo/1.0.0")
		if err != nil {
			log.Fatalf("打开流失败: %v", err)
		}

		_, err = s.Write([]byte("你好，这是通过KCP传输的消息!\n"))
		if err != nil {
			log.Fatalf("写入失败: %v", err)
		}

		s.Close()
	} else {
		// 设置流处理程序
		h.SetStreamHandler("/kcp-demo/1.0.0", func(s network.Stream) {
			buf := make([]byte, 1024)
			n, err := s.Read(buf)
			if err != nil {
				log.Printf("读取错误: %v", err)
				s.Close()
				return
			}

			fmt.Printf("收到来自 %s 的消息: %s", s.Conn().RemotePeer(), string(buf[:n]))
			s.Close()
		})

		fmt.Println("等待连接...")
	}

	// 等待中断信号
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	fmt.Println("正在关闭...")
	h.Close()
}
