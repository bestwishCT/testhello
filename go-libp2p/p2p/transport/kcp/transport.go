package libp2pkcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
	kcp "github.com/xtaci/kcp-go"
)

const ListenOrder = 1

var log = logging.Logger("kcp-transport")

// 支持的加密方式
const (
	EncryptNone      = "none"
	EncryptAES       = "aes"
	EncryptXTEA      = "xtea"
	EncryptSalsa20   = "salsa20"
	EncryptBlowfish  = "blowfish"
	EncryptTwofish   = "twofish"
	EncryptCast5     = "cast5"
	EncryptTripleDES = "3des"
	EncryptTEA       = "tea"
	EncryptSM4       = "sm4"
	EncryptSimpleXOR = "xor"
)

// The Transport implements the tpt.Transport interface for KCP connections.
type transport struct {
	privKey   ic.PrivKey
	localPeer peer.ID
	gater     connmgr.ConnectionGater
	rcmgr     network.ResourceManager

	connMx sync.Mutex
	conns  map[*kcp.UDPSession]*conn

	listenersMu sync.Mutex
	listeners   map[string][]*listener

	// KCP配置参数
	kcpConfig KCPConfig
}

// KCP配置参数结构体
type KCPConfig struct {
	NoDelay      int  // 是否启用 NoDelay模式，0不启用，1启用
	Interval     int  // 内部update时钟间隔，单位毫秒
	Resend       int  // 快速重传模式，0关闭，1开启
	NoCongestion int  // 是否关闭拥塞控制，0不关闭，1关闭
	SndWnd       int  // 发送窗口大小
	RcvWnd       int  // 接收窗口大小
	MTU          int  // 最大传输单元
	StreamMode   bool // 是否启用流模式
	WriteDelay   bool // 是否启用写延迟
	AckNoDelay   bool // 是否启用无延迟ACK
}

// 默认KCP配置
var DefaultKCPConfig = KCPConfig{
	NoDelay:      1,     // 启用NoDelay
	Interval:     10,    // 10ms更新间隔
	Resend:       2,     // 快速重传
	NoCongestion: 1,     // 关闭拥塞控制
	SndWnd:       32,    // 发送窗口
	RcvWnd:       32,    // 接收窗口
	MTU:          1400,  // 默认MTU
	StreamMode:   true,  // 启用流模式
	WriteDelay:   false, // 不启用写延迟
	AckNoDelay:   true,  // 启用无延迟ACK
}

var _ tpt.Transport = &transport{}

// NewTransport creates a new KCP transport
func NewTransport(key ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager) (tpt.Transport, error) {
	if len(psk) > 0 {
		log.Error("KCP doesn't support private networks yet.")
		return nil, errors.New("KCP doesn't support private networks yet")
	}

	localPeer, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, err
	}

	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}

	return &transport{
		privKey:   key,
		localPeer: localPeer,
		gater:     gater,
		rcmgr:     rcmgr,
		conns:     make(map[*kcp.UDPSession]*conn),
		listeners: make(map[string][]*listener),
		kcpConfig: DefaultKCPConfig,
	}, nil
}

// NewTransportWithConfig creates a new KCP transport with custom KCP configuration
func NewTransportWithConfig(key ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, config KCPConfig) (tpt.Transport, error) {
	if len(psk) > 0 {
		log.Error("KCP doesn't support private networks yet.")
		return nil, errors.New("KCP doesn't support private networks yet")
	}

	localPeer, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, err
	}

	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}

	return &transport{
		privKey:   key,
		localPeer: localPeer,
		gater:     gater,
		rcmgr:     rcmgr,
		conns:     make(map[*kcp.UDPSession]*conn),
		listeners: make(map[string][]*listener),
		kcpConfig: config,
	}, nil
}

// SetKCPConfig sets the KCP configuration parameters
func (t *transport) SetKCPConfig(config KCPConfig) {
	t.kcpConfig = config
}

func (t *transport) ListenOrder() int {
	return ListenOrder
}

// Dial dials a new KCP connection
func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	c, err := t.dialWithScope(ctx, raddr, p, scope)
	if err != nil {
		scope.Done()
		return nil, err
	}
	return c, nil
}

func (t *transport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	log.Debugf("KCP: 开始拨号 远程地址=%s 远程节点=%s", raddr, p)
	if err := scope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	_, saddr, err := manet.DialArgs(raddr)
	if err != nil {
		log.Debugf("KCP: 解析拨号参数失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 拨号地址: %s", saddr)

	// 从地址中提取加密方式和密钥
	encType, encKey, err := extractEncryptionParams(raddr)
	if err != nil {
		log.Debugf("KCP: 提取加密参数失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 使用加密方式=%s 密钥长度=%d", encType, len(encKey))

	// 创建加密器
	block, err := createBlockCrypt(encType, encKey)
	if err != nil {
		log.Debugf("KCP: 创建加密器失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 加密器创建成功")

	// 拨号连接
	log.Debugf("KCP: 开始KCP拨号到 %s", saddr)
	kcpConn, err := kcp.DialWithOptions(saddr, block, 10, 3)
	if err != nil {
		log.Debugf("KCP: 拨号失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 拨号成功 本地=%s 远程=%s",
		kcpConn.LocalAddr().String(), kcpConn.RemoteAddr().String())

	// 设置KCP参数
	kcpConn.SetStreamMode(t.kcpConfig.StreamMode)
	kcpConn.SetWriteDelay(t.kcpConfig.WriteDelay)
	kcpConn.SetNoDelay(t.kcpConfig.NoDelay, t.kcpConfig.Interval, t.kcpConfig.Resend, t.kcpConfig.NoCongestion)
	kcpConn.SetACKNoDelay(t.kcpConfig.AckNoDelay)
	kcpConn.SetWindowSize(t.kcpConfig.SndWnd, t.kcpConfig.RcvWnd)
	kcpConn.SetMtu(t.kcpConfig.MTU)
	log.Debugf("KCP: 已设置KCP参数 NoDelay=%d Interval=%d Resend=%d NoCongestion=%d SndWnd=%d RcvWnd=%d MTU=%d StreamMode=%v WriteDelay=%v AckNoDelay=%v",
		t.kcpConfig.NoDelay, t.kcpConfig.Interval, t.kcpConfig.Resend, t.kcpConfig.NoCongestion,
		t.kcpConfig.SndWnd, t.kcpConfig.RcvWnd, t.kcpConfig.MTU,
		t.kcpConfig.StreamMode, t.kcpConfig.WriteDelay, t.kcpConfig.AckNoDelay)

	localMultiaddr, err := maFromUDPAddr(kcpConn.LocalAddr().(*net.UDPAddr), encType, encKey)
	if err != nil {
		log.Debugf("KCP: 创建本地多地址失败: %v", err)
		kcpConn.Close()
		return nil, err
	}
	log.Debugf("KCP: 本地多地址: %s", localMultiaddr)

	c := &conn{
		kcpConn:         kcpConn,
		transport:       t,
		scope:           scope,
		localPeer:       t.localPeer,
		localMultiaddr:  localMultiaddr,
		remotePubKey:    nil, // 没有TLS，所以没有公钥
		remotePeerID:    p,
		remoteMultiaddr: raddr,
		encType:         encType,
		encKey:          encKey,
		isServer:        false, // 这是一个拨号连接，不是服务器端
	}
	log.Infof("KCP: 已创建连接对象 本地节点=%s 远程节点=%s", t.localPeer, p)

	// 设置yamux多路复用会话
	if err := c.setupYamux(); err != nil {
		log.Debugf("KCP: 设置yamux失败: %v", err)
		kcpConn.Close()
		return nil, err
	}
	log.Debugf("KCP: yamux会话已设置")

	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, c) {
		log.Debugf("KCP: 连接被连接门控器拒绝")
		kcpConn.Close()
		return nil, fmt.Errorf("secured connection gated")
	}

	t.addConn(kcpConn, c)
	log.Debugf("KCP: 连接已添加到连接管理器")
	// 记录连接详情
	c.logConnectionDetails()
	return c, nil
}

func (t *transport) addConn(kcpConn *kcp.UDPSession, c *conn) {
	t.connMx.Lock()
	t.conns[kcpConn] = c
	t.connMx.Unlock()
}

func (t *transport) removeConn(kcpConn *kcp.UDPSession) {
	t.connMx.Lock()
	delete(t.conns, kcpConn)
	t.connMx.Unlock()
}

// 定义KCP地址匹配器，匹配格式: /ip{4,6}/udp/kcp/enc/{加密方式}/key/{key}
var dialMatcher = mafmt.And(mafmt.IP, mafmt.Base(ma.P_UDP), mafmt.Base(P_KCP), mafmt.Base(P_ENC), mafmt.Base(P_KEY))

// CanDial determines if we can dial to an address
func (t *transport) CanDial(addr ma.Multiaddr) bool {
	return dialMatcher.Matches(addr)
}

// Listen listens for new KCP connections on the passed multiaddr.
func (t *transport) Listen(addr ma.Multiaddr) (tpt.Listener, error) {
	log.Debugf("KCP: 开始监听 地址=%s", addr)
	_, saddr, err := manet.DialArgs(addr)
	if err != nil {
		log.Debugf("KCP: 解析监听参数失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 监听地址: %s", saddr)

	// 从地址中提取加密方式和密钥
	encType, encKey, err := extractEncryptionParams(addr)
	if err != nil {
		log.Debugf("KCP: 提取加密参数失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 使用加密方式=%s 密钥长度=%d", encType, len(encKey))

	// 创建加密器
	block, err := createBlockCrypt(encType, encKey)
	if err != nil {
		log.Debugf("KCP: 创建加密器失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 加密器创建成功")

	// 监听连接
	log.Debugf("KCP: 开始KCP监听 %s", saddr)
	kcpListener, err := kcp.ListenWithOptions(saddr, block, 10, 3)
	if err != nil {
		log.Debugf("KCP: 监听失败: %v", err)
		return nil, err
	}
	log.Debugf("KCP: 监听成功 地址=%s", kcpListener.Addr().String())

	// 创建监听器
	l, err := newListener(kcpListener, t, t.localPeer, t.privKey, t.rcmgr, encType, encKey, addr)
	if err != nil {
		log.Debugf("KCP: 创建监听器失败: %v", err)
		kcpListener.Close()
		return nil, err
	}
	log.Infof("KCP: 监听器创建成功 地址=%s 节点=%s", addr, t.localPeer)

	return l, nil
}

// Proxy returns true if this transport proxies.
func (t *transport) Proxy() bool {
	return false
}

// Protocols returns the set of protocols handled by this transport.
func (t *transport) Protocols() []int {
	return []int{P_KCP, P_ENC, P_KEY}
}

func (t *transport) String() string {
	return "KCP"
}

func (t *transport) Close() error {
	log.Debugf("KCP: 开始关闭传输层")

	t.connMx.Lock()
	conns := make([]*conn, 0, len(t.conns))
	for _, c := range t.conns {
		conns = append(conns, c)
	}
	t.connMx.Unlock()

	// 逐个关闭连接，而不是在锁内关闭
	for _, c := range conns {
		log.Debugf("KCP: 关闭连接 %s -> %s", c.localMultiaddr, c.remoteMultiaddr)
		c.closeWithError(0, "transport closing")
	}

	// 等待一段时间，确保所有连接都有机会完成关闭
	log.Debugf("KCP: 等待连接完成关闭")
	time.Sleep(200 * time.Millisecond)

	log.Debugf("KCP: 传输层关闭完成")
	return nil
}

// 辅助函数：从多地址中提取加密参数
func extractEncryptionParams(addr ma.Multiaddr) (string, []byte, error) {
	// 检查KCP协议是否存在
	_, err := addr.ValueForProtocol(P_KCP)
	if err != nil {
		return "", nil, err
	}

	// 获取加密算法
	encType, err := addr.ValueForProtocol(P_ENC)
	if err != nil {
		return "", nil, err
	}

	// 获取密钥
	keyStr, err := addr.ValueForProtocol(P_KEY)
	if err != nil {
		return "", nil, err
	}

	return encType, []byte(keyStr), nil
}

// 辅助函数：创建加密器
func createBlockCrypt(encType string, key []byte) (kcp.BlockCrypt, error) {
	switch encType {
	case EncryptNone:
		return kcp.NewNoneBlockCrypt(key)
	case EncryptAES:
		return kcp.NewAESBlockCrypt(key)
	case EncryptTEA:
		return kcp.NewTEABlockCrypt(key)
	case EncryptXTEA:
		return kcp.NewXTEABlockCrypt(key)
	case EncryptSalsa20:
		return kcp.NewSalsa20BlockCrypt(key)
	case EncryptBlowfish:
		return kcp.NewBlowfishBlockCrypt(key)
	case EncryptTwofish:
		return kcp.NewTwofishBlockCrypt(key)
	case EncryptCast5:
		return kcp.NewCast5BlockCrypt(key)
	case EncryptTripleDES:
		return kcp.NewTripleDESBlockCrypt(key)
	case EncryptSM4:
		return kcp.NewSM4BlockCrypt(key)
	case EncryptSimpleXOR:
		return kcp.NewSimpleXORBlockCrypt(key)
	default:
		return nil, fmt.Errorf("unsupported encryption type: %s", encType)
	}
}

// 辅助函数: 从UDP地址创建multiaddr
func maFromUDPAddr(addr *net.UDPAddr, encType string, encKey []byte) (ma.Multiaddr, error) {
	// 基本地址
	var base ma.Multiaddr
	var err error
	if addr.IP.To4() != nil {
		base, err = ma.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/%d", addr.IP.String(), addr.Port))
	} else {
		base, err = ma.NewMultiaddr(fmt.Sprintf("/ip6/%s/udp/%d", addr.IP.String(), addr.Port))
	}
	if err != nil {
		return nil, err
	}

	// 添加KCP协议（无值）
	kcpComp, err := ma.NewComponent("kcp", "")
	if err != nil {
		return nil, err
	}

	// 添加加密算法
	encComp, err := ma.NewComponent("enc", encType)
	if err != nil {
		return nil, err
	}

	// 添加密钥
	keyComp, err := ma.NewComponent("key", string(encKey))
	if err != nil {
		return nil, err
	}

	// 组合地址
	return base.Encapsulate(kcpComp).Encapsulate(encComp).Encapsulate(keyComp), nil
}
