package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// EncryptAES 使用AES-CBC模式加密数据，返回base64编码的密文
func EncryptAES(plaintext string, key, iv []byte) (string, error) {
	// 创建AES加密器
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("创建AES密码块失败: %w", err)
	}

	// 添加PKCS#7填充
	padding := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padtext := make([]byte, len(plaintext)+padding)
	copy(padtext, plaintext)
	for i := len(plaintext); i < len(padtext); i++ {
		padtext[i] = byte(padding)
	}

	// 加密数据
	encrypted := make([]byte, len(padtext))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(encrypted, padtext)

	// Base64编码
	encryptedBase64 := base64.StdEncoding.EncodeToString(encrypted)
	return encryptedBase64, nil
}

// DecryptAES 使用AES-CBC模式解密base64编码的数据
func DecryptAES(encryptedBase64 string, key, iv []byte) (string, error) {
	// 解码base64
	encryptedData, err := base64.StdEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return "", fmt.Errorf("base64解码失败: %w", err)
	}

	// 创建AES解密器
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("创建AES密码块失败: %w", err)
	}

	// 检查数据长度
	if len(encryptedData) < aes.BlockSize {
		return "", fmt.Errorf("加密数据太短")
	}

	// 创建CBC模式解密器
	mode := cipher.NewCBCDecrypter(block, iv)

	// 解密数据
	decrypted := make([]byte, len(encryptedData))
	mode.CryptBlocks(decrypted, encryptedData)

	// 去除PKCS#7填充
	padding := int(decrypted[len(decrypted)-1])
	if padding > aes.BlockSize || padding == 0 {
		return "", fmt.Errorf("无效的填充")
	}

	// 验证填充
	for i := len(decrypted) - padding; i < len(decrypted); i++ {
		if decrypted[i] != byte(padding) {
			return "", fmt.Errorf("填充验证失败")
		}
	}

	// 去除填充
	decrypted = decrypted[:len(decrypted)-padding]

	// 转换为字符串并返回
	return string(decrypted), nil
}

// GenerateRandomKey 生成随机密钥
func GenerateRandomKey(length int) ([]byte, error) {
	key := make([]byte, length)
	_, err := rand.Read(key)
	if err != nil {
		return nil, fmt.Errorf("生成随机密钥失败: %w", err)
	}
	return key, nil
}

// GenerateRandomIV 生成随机初始向量
func GenerateRandomIV() ([]byte, error) {
	iv := make([]byte, aes.BlockSize)
	_, err := rand.Read(iv)
	if err != nil {
		return nil, fmt.Errorf("生成随机IV失败: %w", err)
	}
	return iv, nil
}
