package common

import (
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	APIProxyProtocolID    = protocol.ID("/p2p/api-proxy/1.0.0")    // API代理协议
	TunnelProxyProtocolID = protocol.ID("/p2p/tunnel-proxy/1.0.0") // 隧道代理协议
	BusinessServer        = "194.68.32.224:8089"
)
