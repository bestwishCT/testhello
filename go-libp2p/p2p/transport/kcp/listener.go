package libp2pkcp

import (
	"net"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	kcp "github.com/xtaci/kcp-go"
)

// A listener listens for KCP connections.
type listener struct {
	kcpListener    *kcp.Listener
	transport      *transport
	rcmgr          network.ResourceManager
	privKey        ic.PrivKey
	localPeer      peer.ID
	localMultiaddr ma.Multiaddr
	encType        string
	encKey         []byte
}

var _ tpt.Listener = &listener{}

func newListener(kcpListener *kcp.Listener, t *transport, localPeer peer.ID, key ic.PrivKey, rcmgr network.ResourceManager, encType string, encKey []byte, addr ma.Multiaddr) (*listener, error) {
	return &listener{
		kcpListener:    kcpListener,
		transport:      t,
		rcmgr:          rcmgr,
		privKey:        key,
		localPeer:      localPeer,
		localMultiaddr: addr,
		encType:        encType,
		encKey:         encKey,
	}, nil
}

// Accept accepts new connections.
func (l *listener) Accept() (tpt.CapableConn, error) {
	log.Debugf("KCP: 开始接受新连接 监听地址=%s", l.localMultiaddr)
	for {
		log.Debugf("KCP: 等待接受KCP连接...")
		kcpConn, err := l.kcpListener.AcceptKCP()
		if err != nil {
			log.Debugf("KCP: 接受连接失败: %v", err)
			return nil, err
		}
		log.Debugf("KCP: 接受了新的KCP连接 来自=%s", kcpConn.RemoteAddr().String())

		// 配置KCP连接
		kcpConn.SetStreamMode(l.transport.kcpConfig.StreamMode)
		kcpConn.SetWriteDelay(l.transport.kcpConfig.WriteDelay)
		kcpConn.SetNoDelay(l.transport.kcpConfig.NoDelay, l.transport.kcpConfig.Interval, l.transport.kcpConfig.Resend, l.transport.kcpConfig.NoCongestion)
		kcpConn.SetACKNoDelay(l.transport.kcpConfig.AckNoDelay)
		kcpConn.SetWindowSize(l.transport.kcpConfig.SndWnd, l.transport.kcpConfig.RcvWnd)
		kcpConn.SetMtu(l.transport.kcpConfig.MTU)
		log.Debugf("KCP: 已配置KCP连接参数 NoDelay=%d Interval=%d Resend=%d NoCongestion=%d SndWnd=%d RcvWnd=%d MTU=%d StreamMode=%v WriteDelay=%v AckNoDelay=%v",
			l.transport.kcpConfig.NoDelay, l.transport.kcpConfig.Interval, l.transport.kcpConfig.Resend, l.transport.kcpConfig.NoCongestion,
			l.transport.kcpConfig.SndWnd, l.transport.kcpConfig.RcvWnd, l.transport.kcpConfig.MTU,
			l.transport.kcpConfig.StreamMode, l.transport.kcpConfig.WriteDelay, l.transport.kcpConfig.AckNoDelay)

		log.Debugf("KCP: 开始设置连接...")
		c, err := l.setupConn(kcpConn)
		if err != nil {
			log.Debugf("KCP: 设置连接失败: %v", err)
			kcpConn.Close()
			continue
		}

		l.transport.addConn(kcpConn, c)
		log.Debugf("KCP: 连接已添加到传输层")

		// 检查连接门控器
		if l.transport.gater != nil {
			log.Debugf("KCP: 检查连接门控器")
			interceptAccept := l.transport.gater.InterceptAccept(c)
			interceptSecured := l.transport.gater.InterceptSecured(network.DirInbound, c.remotePeerID, c)
			log.Debugf("KCP: 连接门控器结果 accept=%v secured=%v", interceptAccept, interceptSecured)

			if !(interceptAccept && interceptSecured) {
				log.Debugf("KCP: 连接被门控器拒绝")
				c.closeWithError(0, "connection gated")
				continue
			}
		}

		log.Infof("KCP: 连接接受完成 远程地址=%s 远程节点=%s", c.remoteMultiaddr, c.remotePeerID)
		// 记录连接详情
		c.logConnectionDetails()
		return c, nil
	}
}

// setupConn sets up a newly accepted connection
func (l *listener) setupConn(kcpConn *kcp.UDPSession) (*conn, error) {
	log.Debugf("KCP: 设置接受的连接 远程地址=%s", kcpConn.RemoteAddr().String())

	// 计算远程地址
	remoteMultiaddr, err := maFromUDPAddr(kcpConn.RemoteAddr().(*net.UDPAddr), l.encType, l.encKey)
	if err != nil {
		log.Debugf("KCP: 创建远程多地址失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 远程多地址: %s", remoteMultiaddr)

	// 使用PeerID作为远程标识
	// 注意：在没有TLS的情况下，我们没有办法验证对等方身份
	// 这里我们使用KCP加密密钥作为标识符的一部分生成临时PeerID
	remotePeerIDStr := "kcp-" + l.encType + "-" + string(l.encKey) + "-" + kcpConn.RemoteAddr().String()
	remotePeerID, err := peer.Decode(peer.ID(remotePeerIDStr).String())
	if err != nil {
		remotePeerID = peer.ID(remotePeerIDStr)
		log.Debugf("KCP: 无法解码远程节点ID，使用原始字符串: %s", remotePeerIDStr)
	}
	log.Debugf("KCP: 远程节点ID: %s", remotePeerID)

	// 创建连接管理作用域
	log.Debugf("KCP: 创建连接管理作用域")
	connScope, err := l.rcmgr.OpenConnection(network.DirInbound, false, remoteMultiaddr)
	if err != nil {
		log.Debugw("resource manager blocked incoming connection", "addr", kcpConn.RemoteAddr(), "error", err)
		return nil, err
	}

	if err := connScope.SetPeer(remotePeerID); err != nil {
		log.Debugw("resource manager blocked incoming connection for peer", "peer", remotePeerID, "addr", kcpConn.RemoteAddr(), "error", err)
		connScope.Done()
		return nil, err
	}
	log.Debugf("KCP: 连接管理作用域设置成功")

	// 创建连接
	log.Debugf("KCP: 创建连接对象 本地节点=%s 远程节点=%s", l.localPeer, remotePeerID)
	c := &conn{
		kcpConn:         kcpConn,
		transport:       l.transport,
		scope:           connScope,
		localPeer:       l.localPeer,
		localMultiaddr:  l.localMultiaddr,
		remoteMultiaddr: remoteMultiaddr,
		remotePeerID:    remotePeerID,
		remotePubKey:    nil, // 没有TLS，所以没有远程公钥
		encType:         l.encType,
		encKey:          l.encKey,
		isServer:        true, // 这是一个接受的连接，是服务器端
	}

	// 设置yamux多路复用会话
	if err := c.setupYamux(); err != nil {
		log.Debugf("KCP: 设置yamux失败: %v", err)
		connScope.Done()
		return nil, err
	}
	log.Debugf("KCP: yamux会话已设置")

	return c, nil
}

// Close closes the listener.
func (l *listener) Close() error {
	log.Debugf("KCP: 关闭监听器 %s", l.localMultiaddr)

	// 安全地关闭KCP监听器
	err := l.kcpListener.Close()
	if err != nil {
		log.Debugf("KCP: 关闭监听器时出错: %v", err)
	}

	// 给读写操作一点时间优雅关闭
	time.Sleep(100 * time.Millisecond)

	log.Debugf("KCP: 监听器关闭完成 %s", l.localMultiaddr)
	return err
}

// Addr returns the address of this listener.
func (l *listener) Addr() net.Addr {
	return l.kcpListener.Addr()
}

// Multiaddr returns the multiaddress of this listener.
func (l *listener) Multiaddr() ma.Multiaddr {
	return l.localMultiaddr
}
