package relay

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"

	"shiledp2p/pkg/common"
)

// ProxyService 提供代理服务
type ProxyService struct {
	host host.Host
	ctx  context.Context
	wg   sync.WaitGroup
}

// NewProxyService 创建新的代理服务
func NewProxyService(h host.Host) *ProxyService {
	return &ProxyService{
		host: h,
		ctx:  context.Background(),
	}
}

// Start 启动代理服务
func (p *ProxyService) Start() {
	// 注册代理协议处理器
	common.RegisterStreamHandler(p.host, common.ProxyProtocolID, p.handleProxyRequest)
	common.RegisterStreamHandler(p.host, common.APIProxyProtocolID, p.handleProxyStream)
	common.RegisterStreamHandler(p.host, common.TunnelProxyProtocolID, p.handleProxyStream)
	fmt.Println("代理服务已启动")
}

// Stop 停止代理服务
func (p *ProxyService) Stop(ctx context.Context) {
	doneCh := make(chan struct{})

	go func() {
		p.wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		return
	case <-ctx.Done():
		fmt.Println("代理服务停止超时")
		return
	}
}

// 修改: 处理代理流
func (p *ProxyService) handleProxyStream(s network.Stream) {
	// 获取远程节点信息
	remotePeer := s.Conn().RemotePeer()
	fmt.Printf("[代理] 收到来自 %s 的请求，协议: %s\n", remotePeer.String(), s.Protocol())

	// 根据流协议类型选择不同的处理方式
	switch s.Protocol() {
	case common.APIProxyProtocolID:
		fmt.Printf("[代理] 处理API代理请求\n")
		p.handleAPIProxyStream(s)
	case common.TunnelProxyProtocolID:
		fmt.Printf("[代理] 处理隧道代理请求\n")
		//p.handleTunnelProxyStream(s)
		p.handleProxyRequest(s) // 使用通用处理函数
	default:
		fmt.Printf("[代理] 未知的协议类型: %s\n", s.Protocol())
		s.Close()
	}
}

// 处理API代理流 - 转发到业务服务器
func (p *ProxyService) handleAPIProxyStream(s network.Stream) {
	defer s.Close()

	// 获取远程节点信息
	remotePeer := s.Conn().RemotePeer()
	fmt.Printf("[API代理] 收到来自 %s 的业务API请求\n", remotePeer.String())

	// 创建到业务服务器的连接
	targetConn, err := net.Dial("tcp", "194.68.32.224:8089")
	if err != nil {
		fmt.Printf("[API代理] 连接业务服务器失败: %v\n", err)
		return
	}
	defer targetConn.Close()

	// 双向转发数据
	var wg sync.WaitGroup
	wg.Add(2)

	// 客户端 -> 业务服务器
	go func() {
		defer wg.Done()
		io.Copy(targetConn, s)
	}()

	// 业务服务器 -> 客户端
	go func() {
		defer wg.Done()
		io.Copy(s, targetConn)
	}()

	wg.Wait()
	fmt.Printf("[API代理] 与 %s 的业务API代理连接已关闭\n", remotePeer.String())
}

// handleProxyRequest 处理代理请求(处理隧道代理流 - 实现通用HTTP/HTTPS代理)
func (p *ProxyService) handleProxyRequest(s network.Stream) {
	p.wg.Add(1)
	defer p.wg.Done()

	// 获取发送者信息，用于日志
	remotePeer := s.Conn().RemotePeer()
	remoteAddr := s.Conn().RemoteMultiaddr()
	fmt.Printf("\n====== 收到代理请求 ======\n")
	fmt.Printf("请求来自: %s (%s)\n", remotePeer.String(), remoteAddr.String())
	fmt.Printf("流ID: %s\n", s.ID())
	fmt.Printf("协议: %s\n", s.Protocol())
	fmt.Printf("连接方向: %s\n", s.Conn().Stat().Direction)

	// 设置读取超时 - 延长到15秒等待数据
	fmt.Println("等待接收数据，超时时间15秒...")
	s.SetReadDeadline(time.Now().Add(15 * time.Second))

	// 多次尝试读取数据，最多3次
	var requestData []byte
	var n int
	var err error
	maxTries := 3

	for tries := 0; tries < maxTries; tries++ {
		// 读取最初的请求头，最多读取8K（足够HTTP头部）
		buf := make([]byte, 8192)
		n, err = s.Read(buf)

		// 如果成功读取到数据，则退出循环
		if n > 0 {
			requestData = buf[:n]
			fmt.Printf("尝试 #%d: 成功读取 %d 字节数据\n", tries+1, n)
			break
		}

		// 如果错误不是超时，则退出循环
		if err != nil && !strings.Contains(err.Error(), "deadline") {
			fmt.Printf("尝试 #%d: 读取错误: %v\n", tries+1, err)
			break
		}

		fmt.Printf("尝试 #%d: 未读取到数据，重试...\n", tries+1)
		// 减少每次尝试的等待时间
		s.SetReadDeadline(time.Now().Add(5 * time.Second))
	}

	// 重置超时
	s.SetReadDeadline(time.Time{})

	// 详细记录错误信息
	if err != nil {
		if err == io.EOF {
			fmt.Printf("读取请求时收到EOF，可能是空请求或连接已关闭，但已读取 %d 字节\n", n)
		} else {
			fmt.Printf("读取请求数据失败: %v (读取了 %d 字节)\n", err, n)

			// 打印错误类型和详细信息
			fmt.Printf("错误类型: %T\n", err)
			if netErr, ok := err.(net.Error); ok {
				fmt.Printf("网络错误: 超时=%v, 临时=%v\n", netErr.Timeout(), netErr.Temporary())
			}

			s.Close()
			return
		}
	}

	if n == 0 {
		fmt.Println("请求为空，无法处理")
		s.Close()
		return
	}

	// 获取请求数据
	fmt.Printf("读取到 %d 字节的请求数据\n", n)

	// 转换为十六进制打印，便于查看二进制数据
	fmt.Println("请求数据十六进制表示:")
	for i := 0; i < n; i += 16 {
		end := i + 16
		if end > n {
			end = n
		}
		chunk := requestData[i:end]
		hexStr := ""
		asciiStr := ""

		for j, b := range chunk {
			hexStr += fmt.Sprintf("%02x ", b)
			if b >= 32 && b <= 126 { // 可打印ASCII字符
				asciiStr += string(b)
			} else {
				asciiStr += "."
			}
			// 在第8个字节后添加一个空格，提高可读性
			if j == 7 {
				hexStr += " "
			}
		}

		// 补齐空格对齐
		for len(hexStr) < 49 { // 16*3 + 1
			hexStr += "   "
		}

		fmt.Printf("%04x: %s | %s\n", i, hexStr, asciiStr)
	}

	// 打印请求文本内容
	if n > 100 {
		fmt.Printf("请求数据预览(前100字节): %s\n", string(requestData[:100]))
		fmt.Printf("请求数据预览(后100字节): %s\n", string(requestData[n-100:]))
	} else {
		fmt.Printf("请求数据全文: %s\n", string(requestData))
	}

	// 尝试解析请求头
	headers := parseHeaders(requestData)
	fmt.Println("\n===== 解析出的HTTP请求头 =====")
	for k, v := range headers {
		fmt.Printf("%s: %s\n", k, v)
	}
	fmt.Println("=============================")

	// 解析请求获取目标地址信息
	targetHost, targetPort, isHttps := parseHTTPRequest(requestData)

	if targetHost == "" {
		fmt.Println("无法从请求中解析出目标主机")
		fmt.Println("详细分析请求第一行:")
		lines := strings.Split(string(requestData), "\r\n")
		if len(lines) > 0 {
			fmt.Printf("- 第一行: '%s'\n", lines[0])
			parts := strings.Split(lines[0], " ")
			fmt.Printf("- 分割后: %v (共%d部分)\n", parts, len(parts))
		} else {
			fmt.Println("- 无法按\\r\\n分割")
			fmt.Printf("- 原始数据: '%s'\n", string(requestData))
		}

		// 发送通用错误响应
		s.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 26\r\n\r\n无法确定请求的目标主机"))
		s.Close()
		return
	}

	protocolStr := "HTTP"
	if isHttps {
		protocolStr = "HTTPS"
	}
	fmt.Printf("解析请求: 目标=%s:%d, 协议=%s\n", targetHost, targetPort, protocolStr)

	// 连接到目标服务器
	targetAddr := fmt.Sprintf("%s:%d", targetHost, targetPort)
	fmt.Printf("尝试连接到目标服务器: %s\n", targetAddr)

	// 设置超时连接
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	connStart := time.Now()
	conn, err := dialer.Dial("tcp", targetAddr)
	connDuration := time.Since(connStart)

	if err != nil {
		fmt.Printf("连接目标服务器失败 (耗时: %v): %v\n", connDuration, err)
		// 检查错误类型
		if dnsErr, ok := err.(*net.DNSError); ok {
			fmt.Printf("DNS错误: %v, 临时=%v\n", dnsErr.Name, dnsErr.Temporary())
		} else if opErr, ok := err.(*net.OpError); ok {
			fmt.Printf("操作错误: %v, %v\n", opErr.Op, opErr.Err)
		}

		// 返回错误响应
		errResp := []byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 19\r\n\r\n无法连接目标服务器")
		s.Write(errResp)
		s.Close()
		return
	}

	// 连接成功
	fmt.Printf("成功连接到目标服务器 (耗时: %v)\n", connDuration)

	// 根据协议类型进行处理
	if isHttps {
		// 对于HTTPS，发送连接成功响应
		fmt.Println("HTTPS模式: 发送Connection Established响应")
		s.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	} else {
		// 对于HTTP，我们需要先发送解析出的请求到目标服务器
		fmt.Printf("HTTP模式: 转发原始请求 (%d字节)\n", len(requestData))
		_, err = conn.Write(requestData)
		if err != nil {
			fmt.Printf("发送HTTP请求到目标服务器失败: %v\n", err)
			conn.Close()
			s.Close()
			return
		}
	}

	// 双向转发数据
	var wg sync.WaitGroup
	wg.Add(2)

	// 监控连接状态的通道
	streamClosed := make(chan struct{})
	connClosed := make(chan struct{})

	var targetToClientBytes int64
	var clientToTargetBytes int64

	// 从远程服务器到客户端
	go func() {
		defer wg.Done()
		fmt.Println("启动数据转发: 目标服务器 -> 客户端")
		var err error
		targetToClientBytes, err = io.Copy(s, conn)
		if err != nil && err != io.EOF {
			if strings.Contains(err.Error(), "stream reset") ||
				strings.Contains(err.Error(), "connection closed") ||
				strings.Contains(err.Error(), "use of closed network") {
				fmt.Println("目标服务器到客户端的连接已关闭")
			} else {
				fmt.Printf("目标服务器->客户端数据传输错误: %v\n", err)
			}
		}
		fmt.Printf("目标服务器->客户端数据传输完成: 传输了%d字节\n", targetToClientBytes)
		close(connClosed)
	}()

	// 从客户端到远程服务器
	go func() {
		defer wg.Done()
		fmt.Println("启动数据转发: 客户端 -> 目标服务器")
		var err error
		clientToTargetBytes, err = io.Copy(conn, s)
		if err != nil && err != io.EOF {
			if strings.Contains(err.Error(), "stream reset") ||
				strings.Contains(err.Error(), "connection closed") ||
				strings.Contains(err.Error(), "use of closed network") {
				fmt.Println("客户端到目标服务器的连接已关闭")
			} else {
				fmt.Printf("客户端->目标服务器数据传输错误: %v\n", err)
			}
		}
		fmt.Printf("客户端->目标服务器数据传输完成: 传输了%d字节\n", clientToTargetBytes)
		close(streamClosed)
	}()

	// 等待任一通道有信号
	select {
	case <-connClosed:
		fmt.Println("目标服务器连接已关闭，等待流完成...")
	case <-streamClosed:
		fmt.Println("客户端流已关闭，等待目标服务器连接完成...")
	case <-p.ctx.Done():
		fmt.Println("上下文已取消，关闭所有连接...")
	}

	// 等待所有传输完成
	wg.Wait()

	// 完成后关闭连接
	conn.Close()
	s.Close()

	fmt.Printf("===== 连接统计 =====\n")
	fmt.Printf("从客户端发送到目标: %d字节\n", clientToTargetBytes)
	fmt.Printf("从目标发送到客户端: %d字节\n", targetToClientBytes)
	fmt.Printf("====================\n")
}

// 解析HTTP请求头
func parseHeaders(data []byte) map[string]string {
	headers := make(map[string]string)

	lines := strings.Split(string(data), "\r\n")
	// 跳过第一行(请求行)
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			break // 头部结束
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	return headers
}

// parseHTTPRequest 解析HTTP请求获取目标地址
func parseHTTPRequest(requestData []byte) (host string, port int, isHttps bool) {
	port = 80 // 默认HTTP端口
	requestStr := string(requestData)

	// 检查是否是HTTPS请求 (CONNECT方法)
	if strings.HasPrefix(requestStr, "CONNECT ") {
		isHttps = true
		port = 443 // 默认HTTPS端口

		// 解析CONNECT行 (格式: CONNECT example.com:443 HTTP/1.1)
		firstLine := strings.Split(requestStr, "\r\n")[0]
		parts := strings.Split(firstLine, " ")
		if len(parts) >= 2 {
			hostPort := parts[1]
			hostPortParts := strings.Split(hostPort, ":")
			host = hostPortParts[0]
			if len(hostPortParts) > 1 {
				if p, err := strconv.Atoi(hostPortParts[1]); err == nil {
					port = p
				}
			}
		}
		return
	}

	// 普通HTTP请求，查找Host头
	isHttps = false
	for _, line := range strings.Split(requestStr, "\r\n") {
		if strings.HasPrefix(line, "Host: ") {
			hostVal := line[6:] // "Host: " 的长度是6
			hostPortParts := strings.Split(hostVal, ":")
			host = hostPortParts[0]
			if len(hostPortParts) > 1 {
				if p, err := strconv.Atoi(hostPortParts[1]); err == nil {
					port = p
				}
			}
			break
		}
	}
	return
}
