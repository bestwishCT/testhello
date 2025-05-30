package common

import (
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// GenerateOrLoadPrivateKey 生成新的密钥或加载已有密钥
func GenerateOrLoadPrivateKey(keyPath string) (crypto.PrivKey, error) {
	// 检查密钥文件是否存在
	_, err := os.Stat(keyPath)
	if err == nil {
		// 密钥文件存在，加载它
		return LoadPrivateKey(keyPath)
	} else if !os.IsNotExist(err) {
		// 如果是除了"不存在"之外的错误，返回错误
		return nil, fmt.Errorf("检查密钥文件状态失败: %w", err)
	}

	// 密钥文件不存在，生成新密钥
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("生成密钥对失败: %w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建密钥目录失败: %w", err)
	}

	// 保存密钥
	if err := SavePrivateKey(priv, keyPath); err != nil {
		return nil, err
	}

	return priv, nil
}

// SavePrivateKey 保存私钥到文件
func SavePrivateKey(priv crypto.PrivKey, keyPath string) error {
	// 序列化私钥
	keyBytes, err := priv.Raw()
	if err != nil {
		return fmt.Errorf("序列化私钥失败: %w", err)
	}

	// 编码为十六进制字符串
	keyHex := hex.EncodeToString(keyBytes)

	// 写入文件
	if err := ioutil.WriteFile(keyPath, []byte(keyHex), 0600); err != nil {
		return fmt.Errorf("写入密钥文件失败: %w", err)
	}

	return nil
}

// LoadPrivateKey 从文件加载私钥
func LoadPrivateKey(keyPath string) (crypto.PrivKey, error) {
	// 读取密钥文件
	keyHex, err := ioutil.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件失败: %w", err)
	}

	// 解码十六进制字符串
	keyBytes, err := hex.DecodeString(string(keyHex))
	if err != nil {
		return nil, fmt.Errorf("解码密钥失败: %w", err)
	}

	// 根据密钥类型，解析私钥
	priv, err := crypto.UnmarshalEd25519PrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}

	return priv, nil
}
