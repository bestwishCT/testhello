package libp2pkcp

import (
	"context"
	"errors"
	"net"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	yamux "github.com/libp2p/go-libp2p/p2p/muxer/yamux"

	ma "github.com/multiformats/go-multiaddr"
	kcp "github.com/xtaci/kcp-go"
)

// 自定义错误
var (
	ErrNoYamuxConn = errors.New("no yamux session established")
)

type conn struct {
	kcpConn   *kcp.UDPSession
	yamuxConn network.MuxedConn // yamux会话用于多路复用
	transport *transport
	scope     network.ConnManagementScope

	localPeer      peer.ID
	localMultiaddr ma.Multiaddr

	remotePeerID    peer.ID
	remotePubKey    ic.PubKey
	remoteMultiaddr ma.Multiaddr

	// KCP特有的属性
	encType  string
	encKey   []byte
	isServer bool // 是否是服务端（接受的连接）
}

var _ tpt.CapableConn = &conn{}

// Close closes the connection.
func (c *conn) Close() error {
	log.Debugf("KCP: 关闭连接 local=%s remote=%s", c.localMultiaddr, c.remoteMultiaddr)
	return c.closeWithError(0, "")
}

// CloseWithError closes the connection with an error code.
func (c *conn) CloseWithError(code network.ConnErrorCode) error {
	log.Debugf("KCP: 带错误关闭连接 local=%s remote=%s code=%d", c.localMultiaddr, c.remoteMultiaddr, code)
	return c.closeWithError(uint32(code), "")
}

func (c *conn) closeWithError(code uint32, message string) error {
	log.Debugf("KCP: 执行关闭连接 local=%s remote=%s code=%d message=%s",
		c.localMultiaddr, c.remoteMultiaddr, code, message)

	var yamuxErr, kcpErr error

	// 1. 首先移除连接管理
	c.transport.removeConn(c.kcpConn)

	// 2. 关闭yamux会话（如果有）
	if c.yamuxConn != nil {
		log.Debugf("KCP: 开始关闭yamux会话...")
		yamuxErr = c.yamuxConn.Close()
		if yamuxErr != nil {
			log.Debugf("KCP: 关闭yamux会话出现错误: %v", yamuxErr)
		}

		// 给yamux一点时间完成内部清理
		time.Sleep(100 * time.Millisecond)
	}

	// 3. 关闭底层KCP连接
	log.Debugf("KCP: 开始关闭KCP会话...")
	kcpErr = c.kcpConn.Close()
	if kcpErr != nil {
		log.Debugf("KCP: 关闭KCP会话出现错误: %v", kcpErr)
	}

	// 4. 释放资源管理器资源
	c.scope.Done()

	log.Debugf("KCP: KCP连接关闭完成 local=%s remote=%s yamuxErr=%v kcpErr=%v",
		c.localMultiaddr, c.remoteMultiaddr, yamuxErr, kcpErr)

	// 返回第一个遇到的错误（如果有）
	if yamuxErr != nil {
		return yamuxErr
	}
	return kcpErr
}

// IsClosed returns whether a connection is fully closed.
func (c *conn) IsClosed() bool {
	// 如果yamux会话存在，检查其状态
	if c.yamuxConn != nil {
		return c.yamuxConn.IsClosed()
	}

	// 否则检查KCP连接
	// 简单地返回false，因为KCP没有提供直接检查连接是否关闭的方法
	// 在真实使用中，当连接关闭时，读写操作会返回错误
	return false
}

// OpenStream creates a new stream.
func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	log.Debugf("KCP: 打开新流 local=%s remote=%s", c.localMultiaddr, c.remoteMultiaddr)

	// 通过yamux会话打开流
	if c.yamuxConn != nil {
		return c.yamuxConn.OpenStream(ctx)
	}

	// 如果没有yamux会话（不应该发生），返回错误
	return nil, ErrNoYamuxConn
}

// AcceptStream accepts a stream opened by the other side.
func (c *conn) AcceptStream() (network.MuxedStream, error) {
	log.Debugf("KCP: 接受新流 local=%s remote=%s", c.localMultiaddr, c.remoteMultiaddr)

	// 通过yamux会话接受流
	if c.yamuxConn != nil {
		return c.yamuxConn.AcceptStream()
	}

	// 如果没有yamux会话（不应该发生），返回错误
	return nil, ErrNoYamuxConn
}

// LocalPeer returns our peer ID
func (c *conn) LocalPeer() peer.ID { return c.localPeer }

// RemotePeer returns the peer ID of the remote peer.
func (c *conn) RemotePeer() peer.ID { return c.remotePeerID }

// RemotePublicKey returns the public key of the remote peer.
func (c *conn) RemotePublicKey() ic.PubKey { return c.remotePubKey }

// LocalMultiaddr returns the local Multiaddr associated
func (c *conn) LocalMultiaddr() ma.Multiaddr {
	return c.localMultiaddr
}

// RemoteMultiaddr returns the remote Multiaddr associated
func (c *conn) RemoteMultiaddr() ma.Multiaddr {
	return c.remoteMultiaddr
}

func (c *conn) Transport() tpt.Transport { return c.transport }

func (c *conn) Scope() network.ConnScope { return c.scope }

// ConnState is the state of security connection.
func (c *conn) ConnState() network.ConnectionState {
	state := network.ConnectionState{
		Transport:         "kcp",
		StreamMultiplexer: "yamux", // 添加多路复用器信息
	}
	log.Debugf("KCP: 连接状态 local=%s remote=%s state=%+v",
		c.localMultiaddr, c.remoteMultiaddr, state)
	return state
}

// 添加辅助方法来记录连接详情
func (c *conn) logConnectionDetails() {
	log.Infof("KCP连接详情: 本地[%s %s] 远程[%s %s] 加密[%s]",
		c.localPeer.String(), c.localMultiaddr.String(),
		c.remotePeerID.String(), c.remoteMultiaddr.String(),
		c.encType)

	// KCP-GO库的API可能有变动，这里使用简化的连接信息
	log.Infof("KCP连接状态: 本地端口=%d 远程地址=%s",
		c.kcpConn.LocalAddr().(*net.UDPAddr).Port,
		c.kcpConn.RemoteAddr().String())

	if c.yamuxConn != nil {
		log.Infof("已启用yamux多路复用")
	}
}

// 初始化yamux会话
func (c *conn) setupYamux() error {
	// 使用yamux默认传输对象
	yamuxTransport := yamux.DefaultTransport

	// 使用连接的isServer标志确定是客户端还是服务器
	var muxedConn network.MuxedConn
	var err error

	// 将KCP会话包装为net.Conn接口
	netConn := &kcpNetConn{session: c.kcpConn}

	// 创建yamux会话，传入nil作为scope，让yamux内部创建一个新的scope
	muxedConn, err = yamuxTransport.NewConn(netConn, c.isServer, nil)
	if err != nil {
		return err
	}

	c.yamuxConn = muxedConn
	log.Debugf("KCP: 已设置yamux多路复用会话")
	return nil
}

// kcpNetConn将*kcp.UDPSession适配为net.Conn接口
type kcpNetConn struct {
	session *kcp.UDPSession
}

func (k *kcpNetConn) Read(b []byte) (n int, err error) {
	return k.session.Read(b)
}

func (k *kcpNetConn) Write(b []byte) (n int, err error) {
	return k.session.Write(b)
}

func (k *kcpNetConn) Close() error {
	return k.session.Close()
}

func (k *kcpNetConn) LocalAddr() net.Addr {
	return k.session.LocalAddr()
}

func (k *kcpNetConn) RemoteAddr() net.Addr {
	return k.session.RemoteAddr()
}

func (k *kcpNetConn) SetDeadline(t time.Time) error {
	return k.session.SetDeadline(t)
}

func (k *kcpNetConn) SetReadDeadline(t time.Time) error {
	return k.session.SetReadDeadline(t)
}

func (k *kcpNetConn) SetWriteDeadline(t time.Time) error {
	return k.session.SetWriteDeadline(t)
}
