package common

import (
	"encoding/json"
	"fmt"
	"time"
)

// ReadMessageFromStream 从流中读取消息
func ReadMessageFromStream(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// WriteMessageToStream 将消息写入流
func WriteMessageToStream(v interface{}) ([]byte, error) {
	return JSONMarshalWithNewline(v)
}

// ReadHeartbeatMessage 读取心跳消息
func ReadHeartbeatMessage(data []byte) (HeartbeatMessage, error) {
	var msg HeartbeatMessage
	err := json.Unmarshal(data, &msg)
	if err != nil {
		return msg, fmt.Errorf("解析心跳消息失败: %w", err)
	}
	return msg, nil
}

// IsValidHeartbeatPing 检查是否是有效的心跳ping
func IsValidHeartbeatPing(msg HeartbeatMessage) bool {
	return msg.Action == "ping"
}

// IsValidHeartbeatPong 检查是否是有效的心跳pong
func IsValidHeartbeatPong(msg HeartbeatMessage) bool {
	return msg.Action == "pong"
}

// CreateHeartbeatPing 创建心跳ping消息
func CreateHeartbeatPing(nodeType NodeType, from string) HeartbeatMessage {
	return NewHeartbeatMessage("ping", nodeType, from)
}

// CreateHeartbeatPong 创建心跳pong响应
func CreateHeartbeatPong(nodeTypeStr string, fromID string) HeartbeatMessage {
	// 将字符串转换为NodeType
	var nodeType NodeType
	switch nodeTypeStr {
	case "master", "Master":
		nodeType = TypeMaster
	case "agent", "Agent":
		nodeType = TypeAgent
	case "client", "Client":
		nodeType = TypeClient
	default:
		nodeType = TypeUnknown
	}

	return HeartbeatMessage{
		Action:    "pong",
		Status:    "ok",
		NodeType:  nodeType,
		From:      fromID,
		Timestamp: time.Now().UnixNano(),
	}
}

// CreateHeartbeatError 创建心跳错误响应
func CreateHeartbeatError(errorType string, nodeTypeStr string, fromID string) HeartbeatMessage {
	// 将字符串转换为NodeType
	var nodeType NodeType
	switch nodeTypeStr {
	case "master", "Master":
		nodeType = TypeMaster
	case "agent", "Agent":
		nodeType = TypeAgent
	case "client", "Client":
		nodeType = TypeClient
	default:
		nodeType = TypeUnknown
	}

	return HeartbeatMessage{
		Action:    "error",
		Status:    "error",
		Error:     errorType,
		NodeType:  nodeType,
		From:      fromID,
		Timestamp: time.Now().UnixNano(),
	}
}
