package mobile

import (
	"fmt"
	"time"
)

// SimpleDemo 是一个简单的演示结构体
type SimpleDemo struct {
	name string
}

// NewSimpleDemo 创建一个新的 SimpleDemo 实例
//
//export NewSimpleDemo
func NewSimpleDemo() *SimpleDemo {
	return &SimpleDemo{
		name: "SimpleDemo",
	}
}

// GetName 获取名称
//
//export GetName
func (s *SimpleDemo) GetName() string {
	return s.name
}

// GetTime 获取当前时间
//
//export GetTime
func (s *SimpleDemo) GetTime() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// SayHello 返回一个问候消息
//
//export SayHello
func (s *SimpleDemo) SayHello(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}
