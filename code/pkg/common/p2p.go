package common

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
)

// ConnectToPeer 连接到指定的节点
func ConnectToPeer(ctx context.Context, h host.Host, peerID peer.ID, peerAddr multiaddr.Multiaddr) error {
	// 添加地址到地址簿
	h.Peerstore().AddAddr(peerID, peerAddr, 24*time.Hour)

	// 建立连接
	if err := h.Connect(ctx, peer.AddrInfo{
		ID:    peerID,
		Addrs: []multiaddr.Multiaddr{peerAddr},
	}); err != nil {
		return fmt.Errorf("连接到peer失败 [%s]: %w", peerID.String(), err)
	}

	return nil
}

// CreateNode 创建一个libp2p节点
func CreateNode(
	listenAddrs []multiaddr.Multiaddr,
	enableQuic bool,
	enableTCP bool,
	enableNoise bool,
	enableNAT bool,
	enableHolePunching bool,
) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.ListenAddrs(listenAddrs...),
	}

	// 如果启用QUIC，使用默认传输（包含QUIC）
	if enableQuic {
		opts = append(opts, libp2p.DefaultTransports)
	} else if enableTCP {
		// 如果不启用QUIC但启用TCP，只使用TCP
		opts = append(opts, libp2p.Transport(tcp.NewTCPTransport))
	}

	// 添加安全传输层选项
	if enableNoise {
		opts = append(opts, libp2p.Security(noise.ID, noise.New))
	}

	// 添加NAT穿透选项
	if enableNAT {
		opts = append(opts, libp2p.NATPortMap())
	}

	// 添加打洞选项
	if enableHolePunching {
		opts = append(opts, libp2p.EnableHolePunching())
	}

	// 设置连接管理器
	connManager, err := connmgr.NewConnManager(
		100,                                    // 低水位线
		400,                                    // 高水位线
		connmgr.WithGracePeriod(time.Minute*5), // 宽限期
	)
	if err != nil {
		return nil, fmt.Errorf("创建连接管理器失败: %w", err)
	}
	opts = append(opts, libp2p.ConnectionManager(connManager))

	// 创建libp2p节点
	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("创建libp2p节点失败: %w", err)
	}

	return h, nil
}

// RegisterStreamHandler 注册一个流处理器
func RegisterStreamHandler(h host.Host, protocolID protocol.ID, handler network.StreamHandler) {
	h.SetStreamHandler(protocolID, handler)
}

// GetMultiaddrsString 将多地址列表转换为字符串数组
func GetMultiaddrsString(addrs []multiaddr.Multiaddr) []string {
	result := make([]string, len(addrs))
	for i, addr := range addrs {
		result[i] = addr.String()
	}
	return result
}

// StringsToMultiaddrs 将字符串数组转换为多地址列表
func StringsToMultiaddrs(addrs []string) ([]multiaddr.Multiaddr, error) {
	result := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, addr := range addrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return nil, err
		}
		result = append(result, ma)
	}
	return result, nil
}
