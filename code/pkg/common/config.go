package common

import (
	"sync"
)

// Config 系统配置
type Config struct {
	// TLS SNI 伪装域名
	TlsSNI string `json:"tls_sni"`
	// 注：随机CDN域名功能已直接集成到libp2p库中
}

var (
	config     *Config
	configOnce sync.Once
	configLock sync.RWMutex
)
