package libp2pkcp

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

const (
	// 协议代码，与multiaddr中的协议代码保持一致
	P_KCP = 467 // kcp protocol
	P_ENC = 468 // encryption protocol
	P_KEY = 469 // key protocol
)

// 字符串转码器
type stringTranscoder struct{}

func (t *stringTranscoder) StringToBytes(s string) ([]byte, error) {
	return []byte(s), nil
}

func (t *stringTranscoder) BytesToString(b []byte) (string, error) {
	return string(b), nil
}

func (t *stringTranscoder) ValidateBytes(b []byte) error {
	return nil // 简化实现，任何字节都有效
}

// 导出高性能KCP配置
// 这些配置可以在应用层直接使用
var (
	DefaultConfig = DefaultKCPConfig

	// 普通网络环境，平衡延迟和吞吐量
	NormalNetConfig = KCPConfig{
		NoDelay:      1,     // 启用NoDelay
		Interval:     40,    // 40ms更新间隔
		Resend:       2,     // 快速重传
		NoCongestion: 0,     // 启用拥塞控制
		SndWnd:       128,   // 发送窗口
		RcvWnd:       128,   // 接收窗口
		MTU:          1400,  // 默认MTU
		StreamMode:   true,  // 启用流模式
		WriteDelay:   false, // 不启用写延迟
		AckNoDelay:   true,  // 启用无延迟ACK
	}

	// 快速模式，适合高带宽、低延迟网络
	FastNetConfig = KCPConfig{
		NoDelay:      1,     // 启用NoDelay
		Interval:     10,    // 10ms更新间隔
		Resend:       2,     // 快速重传
		NoCongestion: 1,     // 关闭拥塞控制
		SndWnd:       1024,  // 较大发送窗口
		RcvWnd:       1024,  // 较大接收窗口
		MTU:          1400,  // 默认MTU
		StreamMode:   true,  // 启用流模式
		WriteDelay:   false, // 不启用写延迟
		AckNoDelay:   true,  // 启用无延迟ACK
	}

	// 超级模式，适合10Gbps网络
	UltraNetConfig = KCPConfig{
		NoDelay:      1,     // 启用NoDelay
		Interval:     1,     // 1ms更新间隔
		Resend:       2,     // 快速重传
		NoCongestion: 1,     // 关闭拥塞控制
		SndWnd:       65535, // 超大发送窗口
		RcvWnd:       65535, // 超大接收窗口
		MTU:          1400,  // 标准MTU
		StreamMode:   true,  // 启用流模式
		WriteDelay:   false, // 不启用写延迟
		AckNoDelay:   true,  // 启用无延迟ACK
	}

	// 弱网络环境，更关注可靠性
	WeakNetConfig = KCPConfig{
		NoDelay:      0,     // 不启用NoDelay
		Interval:     100,   // 100ms更新间隔
		Resend:       2,     // 快速重传
		NoCongestion: 0,     // 启用拥塞控制
		SndWnd:       32,    // 较小发送窗口
		RcvWnd:       32,    // 较小接收窗口
		MTU:          1200,  // 较小MTU
		StreamMode:   true,  // 启用流模式
		WriteDelay:   true,  // 启用写延迟
		AckNoDelay:   false, // 不启用无延迟ACK
	}
)

// 便捷函数：创建高性能KCP传输层
func NewUltraTransport(key ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager) (tpt.Transport, error) {
	return NewTransportWithConfig(key, psk, gater, rcmgr, UltraNetConfig)
}

func init() {
	// 注册KCP协议
	const protoKCP = "kcp"
	if err := ma.AddProtocol(ma.Protocol{
		Name:  protoKCP,
		Code:  P_KCP,
		VCode: ma.CodeToVarint(P_KCP),
		Size:  0, // 无值的协议
	}); err != nil {
		panic(fmt.Errorf("failed to register multiaddr protocol %s: %v", protoKCP, err))
	}

	// 注册加密算法协议
	const protoEnc = "enc"
	if err := ma.AddProtocol(ma.Protocol{
		Name:       protoEnc,
		Code:       P_ENC,
		VCode:      ma.CodeToVarint(P_ENC),
		Size:       ma.LengthPrefixedVarSize,
		Transcoder: &stringTranscoder{},
	}); err != nil {
		panic(fmt.Errorf("failed to register multiaddr protocol %s: %v", protoEnc, err))
	}

	// 注册KEY协议
	const protoKey = "key"
	if err := ma.AddProtocol(ma.Protocol{
		Name:       protoKey,
		Code:       P_KEY,
		VCode:      ma.CodeToVarint(P_KEY),
		Size:       ma.LengthPrefixedVarSize,
		Transcoder: &stringTranscoder{},
	}); err != nil {
		panic(fmt.Errorf("failed to register multiaddr protocol %s: %v", protoKey, err))
	}
}
