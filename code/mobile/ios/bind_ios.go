package mobile

import (
	"fmt"
	"log"
	"time"
)

// ShileP2P 是移动端绑定的主结构体
type ShileP2P struct {
	// 基本字段
	version string
}

// NewShileP2P 创建一个新的 ShileP2P 实例
//
//export NewShileP2P
func NewShileP2P() *ShileP2P {
	return &ShileP2P{
		version: "1.0.0",
	}
}

// Start 启动 P2P 服务
//
//export Start
func (s *ShileP2P) Start() error {
	log.Println("Starting ShileP2P service...")
	return nil
}

// Stop 停止 P2P 服务
//
//export Stop
func (s *ShileP2P) Stop() error {
	log.Println("Stopping ShileP2P service...")
	return nil
}

// GetVersion 返回当前版本信息
//
//export GetVersion
func (s *ShileP2P) GetVersion() string {
	return s.version
}

// Echo 用于测试绑定的简单函数
//
//export Echo
func (s *ShileP2P) Echo(message string) string {
	return fmt.Sprintf("Echo: %s", message)
}

// TestHello 返回一个测试消息
//
//export TestHello
func (s *ShileP2P) TestHello() string {
	return fmt.Sprintf("Hello iOS! Server Current time: %s", time.Now().Format("2006-01-02 15:04:05"))
}
