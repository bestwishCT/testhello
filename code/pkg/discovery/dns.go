package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"shiledp2p/pkg/common"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	// DNSDomain 是用于DNS查询的域名
	DNSDomain = "test.halochat.dev"
	// MasterTXTPrefix 是Master节点PeerID的TXT记录前缀
	MasterTXTPrefix = "MASTER="
	// HTTPDNSURL 是阿里云HTTPS DNS服务的URL，使用JSON API接口而不是DNS-query
	HTTPDNSURL = "https://dns.alidns.com/resolve"
	// AES密钥和初始向量 - 实际使用时应从配置中读取或使用更安全的方式存储
	AESKey = "0123456789abcdef0123456789abcdef" // 32字节密钥
	AESIV  = "0123456789abcdef"                 // 16字节IV
	// DNSCacheTTL 是DNS缓存的有效期
	DNSCacheTTL = 30 * time.Minute
)

// aliDNSResponse 定义了阿里云HTTPS DNS JSON API响应的结构
type aliDNSResponse struct {
	Status   int  `json:"Status"`
	TC       bool `json:"TC"`
	RD       bool `json:"RD"`
	RA       bool `json:"RA"`
	AD       bool `json:"AD"`
	CD       bool `json:"CD"`
	Question struct {
		Name string `json:"name"`
		Type int    `json:"type"`
	} `json:"Question"`
	Answer []struct {
		Name string `json:"name"`
		TTL  int    `json:"TTL"`
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// masterAddressInfo 定义Master节点地址信息的结构
type masterAddressInfo struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
	Timestamp  int64    `json:"timestamp"`
}

// DNS查询缓存
var (
	// masterCache 缓存Master节点信息
	masterCache struct {
		peerID     peer.ID
		multiaddrs []multiaddr.Multiaddr
		expiry     time.Time
		mutex      sync.RWMutex
	}
)

// GetMasterPeerID 从DNS TXT记录中获取Master节点的信息，使用HTTPS DNS并解密数据
func GetMasterPeerID(ctx context.Context) (peer.ID, error) {
	// 检查缓存是否有效
	masterCache.mutex.RLock()
	if masterCache.peerID != "" && time.Now().Before(masterCache.expiry) {
		defer masterCache.mutex.RUnlock()
		fmt.Printf("使用缓存的Master PeerID: %s (有效期至: %s)\n",
			masterCache.peerID.String(), masterCache.expiry.Format(time.RFC3339))
		return masterCache.peerID, nil
	}
	masterCache.mutex.RUnlock()

	fmt.Printf("\n====================DNS解析开始====================\n")
	fmt.Printf("使用阿里云HTTPS DNS JSON API服务 %s 查询TXT记录\n", HTTPDNSURL)
	fmt.Printf("查询域名: %s, 记录类型: TXT(16)\n", DNSDomain)

	// 构建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "GET", HTTPDNSURL, nil)
	if err != nil {
		return "", fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 添加查询参数
	q := req.URL.Query()
	q.Add("name", DNSDomain)
	q.Add("type", "16") // TXT记录类型为16
	req.URL.RawQuery = q.Encode()

	fmt.Printf("HTTP请求URL: %s\n", req.URL.String())

	// 发送请求
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTPS DNS查询失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取DNS响应失败: %w", err)
	}

	fmt.Printf("收到HTTP响应: 状态码=%d, 数据长度=%d字节\n", resp.StatusCode, len(body))
	fmt.Printf("响应数据: %s\n", string(body))

	// 解析JSON响应
	var dnsResp aliDNSResponse
	if err := json.Unmarshal(body, &dnsResp); err != nil {
		return "", fmt.Errorf("解析DNS响应JSON失败: %w", err)
	}

	// 检查响应状态
	if dnsResp.Status != 0 {
		return "", fmt.Errorf("DNS查询返回错误状态: %d", dnsResp.Status)
	}

	fmt.Printf("\n找到 %d 条TXT记录\n", len(dnsResp.Answer))
	fmt.Printf("---------------------------------------------------\n")

	// 遍历所有TXT记录
	for i, answer := range dnsResp.Answer {
		if answer.Type != 16 { // TXT记录类型为16
			fmt.Printf("记录 #%d 不是TXT类型(type=%d)，跳过\n", i+1, answer.Type)
			continue
		}

		fmt.Printf("\n处理TXT记录 #%d:\n", i+1)
		fmt.Printf("记录名称: %s\n", answer.Name)
		fmt.Printf("TTL: %d\n", answer.TTL)
		fmt.Printf("原始数据: %s\n", answer.Data)

		// 处理可能的分段TXT记录
		txtData := answer.Data
		// 打印原始格式以便调试
		fmt.Printf("原始格式: %s\n", txtData)

		// 1. 去除所有引号之间的空格，将分段记录拼接
		txtData = strings.ReplaceAll(txtData, "\" \"", "")
		// 2. 再去除两端的引号
		txtData = strings.Trim(txtData, "\"")
		fmt.Printf("处理分段后: %s\n", txtData)

		// 检查是否有MASTER=前缀
		if !strings.HasPrefix(txtData, MasterTXTPrefix) {
			fmt.Printf("TXT记录不包含MASTER=前缀，跳过\n")
			fmt.Printf("---------------------------------------------------\n")
			continue
		}

		// 去除MASTER=前缀，获取加密的Base64部分
		encryptedBase64 := strings.TrimPrefix(txtData, MasterTXTPrefix)
		fmt.Printf("提取的加密Base64数据: %s\n", encryptedBase64)

		// 尝试解码Base64以验证格式
		rawEncrypted, err := base64.StdEncoding.DecodeString(encryptedBase64)
		if err != nil {
			fmt.Printf("Base64解码失败: %v，跳过此记录\n", err)
			fmt.Printf("---------------------------------------------------\n")
			continue
		}
		fmt.Printf("Base64解码后的原始字节数: %d\n", len(rawEncrypted))
		// 打印原始加密字节的16进制表示
		fmt.Printf("加密数据(前32字节，16进制): ")
		maxBytes := 32
		if len(rawEncrypted) < maxBytes {
			maxBytes = len(rawEncrypted)
		}
		for j := 0; j < maxBytes; j++ {
			fmt.Printf("%02x ", rawEncrypted[j])
		}
		if len(rawEncrypted) > maxBytes {
			fmt.Printf("... (共%d字节)\n", len(rawEncrypted))
		} else {
			fmt.Printf("\n")
		}

		// 尝试解密TXT记录
		fmt.Printf("开始AES解密...\n")
		decryptedData, err := common.DecryptAES(encryptedBase64, []byte(AESKey), []byte(AESIV))
		if err != nil {
			fmt.Printf("解密TXT记录失败: %v，尝试下一条记录\n", err)
			fmt.Printf("---------------------------------------------------\n")
			continue
		}

		fmt.Printf("解密后的数据: %s\n", decryptedData)

		// 尝试解析解密后的JSON数据
		var addrInfo masterAddressInfo
		if err := json.Unmarshal([]byte(decryptedData), &addrInfo); err != nil {
			fmt.Printf("解析地址信息JSON失败: %v，尝试下一条记录\n", err)
			fmt.Printf("---------------------------------------------------\n")
			continue
		}
		// TODO 本地测试
		addrInfo = masterAddressInfo{
			PeerID:     "12D3KooWGcBW5nzLfSiGoEvfHfmy1BGwqBLxN99ZdmN5NNFgsCe1",
			Multiaddrs: []string{"/ip4/172.232.239.222/udp/8440/quic-v1/p2p/12D3KooWGcBW5nzLfSiGoEvfHfmy1BGwqBLxN99ZdmN5NNFgsCe1"},
			Timestamp:  time.Now().Unix(),
		}
		// TODO 本地测试
		//addrInfo = masterAddressInfo{
		//	PeerID:     "12D3KooWSSSJYbDMcB2sjuW9ehVeC2QX5rwSMLFanSnHiuqBG9DA",
		//	Multiaddrs: []string{"/ip4/127.0.0.1/udp/8440/quic-v1/p2p/12D3KooWSSSJYbDMcB2sjuW9ehVeC2QX5rwSMLFanSnHiuqBG9DA", "/ip4/192.168.0.70/udp/8440/quic-v1/p2p/12D3KooWSSSJYbDMcB2sjuW9ehVeC2QX5rwSMLFanSnHiuqBG9DA"},
		//	Timestamp:  time.Now().Unix(),
		//}
		// 验证PeerID
		peerID, err := peer.Decode(addrInfo.PeerID)
		if err != nil {
			fmt.Printf("解析PeerID失败: %v，尝试下一条记录\n", err)
			fmt.Printf("---------------------------------------------------\n")
			continue
		}

		fmt.Printf("成功解析Master信息:\n")
		fmt.Printf("  PeerID: %s\n", peerID.String())
		fmt.Printf("  公网地址数量: %d\n", len(addrInfo.Multiaddrs))

		// 解析并缓存多地址
		var multiaddrs []multiaddr.Multiaddr
		for j, addr := range addrInfo.Multiaddrs {
			fmt.Printf("  地址 #%d: %s\n", j+1, addr)

			// 尝试解析多地址
			maddr, err := multiaddr.NewMultiaddr(addr)
			if err == nil {
				multiaddrs = append(multiaddrs, maddr)
			} else {
				fmt.Printf("  警告：地址 #%d 解析失败: %v\n", j+1, err)
			}
		}
		fmt.Printf("  时间戳: %s\n", time.Unix(addrInfo.Timestamp, 0).Format(time.RFC3339))

		// 更新缓存
		masterCache.mutex.Lock()
		masterCache.peerID = peerID
		masterCache.multiaddrs = multiaddrs
		masterCache.expiry = time.Now().Add(DNSCacheTTL)
		masterCache.mutex.Unlock()

		fmt.Printf("Master信息已缓存，有效期至: %s\n", masterCache.expiry.Format(time.RFC3339))
		fmt.Printf("====================DNS解析完成====================\n")
		return peerID, nil
	}

	fmt.Printf("\n未找到可用的Master节点信息\n")
	fmt.Printf("====================DNS解析结束====================\n")
	return "", fmt.Errorf("未找到可用的Master节点信息")
}

// GetMasterMultiaddr 获取Master节点的多地址
// 首先尝试使用缓存中的地址，如果缓存中有多个地址，返回第一个
// 如果缓存为空，则构建默认地址
func GetMasterMultiaddr(masterPeerID peer.ID) (multiaddr.Multiaddr, error) {
	// 尝试从缓存获取多地址
	masterCache.mutex.RLock()
	defer masterCache.mutex.RUnlock()

	// 检查是否有缓存的多地址
	if len(masterCache.multiaddrs) > 0 && time.Now().Before(masterCache.expiry) {
		fmt.Printf("使用缓存的Master多地址: %s\n", masterCache.multiaddrs[0].String())
		return masterCache.multiaddrs[0], nil
	}

	// 缓存中没有或已过期，构建默认地址
	// Master节点的IP和端口
	masterIP := "51.158.154.147"
	masterPort := 8440

	// 构建multiaddr: /ip4/{ip}/udp/{port}/quic-v1/p2p/{peer-id}
	addrStr := fmt.Sprintf("/ip4/%s/udp/%d/quic-v1/p2p/%s",
		masterIP, masterPort, masterPeerID.String())

	fmt.Printf("使用默认Master节点地址: %s\n", addrStr)
	return multiaddr.NewMultiaddr(addrStr)
}
