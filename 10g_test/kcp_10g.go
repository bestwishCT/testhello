// 基于KCP协议和libp2p的10Gbps网络性能测试工具
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	kcp "github.com/libp2p/go-libp2p/p2p/transport/kcp"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
)

// 设置日志记录器
var kcpLogger = logging.Logger("kcp-transport")

// 性能测试结果
type PerfResult struct {
	Throughput   float64 // MB/s
	AvgLatency   float64 // ms
	MaxLatency   float64 // ms
	MinLatency   float64 // ms
	PacketLoss   float64 // %
	TotalBytes   int64
	TotalPackets int
	Duration     time.Duration
	Parallelism  int
}

// 测试配置
type TestConfig struct {
	PacketSize    int           // 每个数据包大小
	TestDuration  time.Duration // 测试持续时间
	Parallelism   int           // 并行数量
	ReportPeriod  time.Duration // 报告周期
	IsLatencyTest bool          // 是否是延迟测试
	Debug         bool          // 是否开启调试模式
}

// 调试日志函数
func debugLog(debug bool, format string, args ...interface{}) {
	if debug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func main() {
	// 设置日志级别
	logging.SetLogLevel("kcp-transport", "debug")
	logging.SetLogLevel("*", "info") // 设置其他组件的日志级别为info

	// 解析命令行参数
	serverMode := flag.Bool("server", false, "以服务端模式运行")
	targetAddr := flag.String("target", "", "目标服务器地址")
	packetSize := flag.Int("size", 64*1024, "数据包大小(字节)")
	testDuration := flag.Duration("duration", 60*time.Second, "测试持续时间")
	parallelism := flag.Int("parallel", 32, "并行流数量")
	reportPeriod := flag.Duration("report", 1*time.Second, "报告周期")
	latencyTest := flag.Bool("latency", false, "执行延迟测试")
	port := flag.Int("port", 12345, "服务端监听端口")
	debugMode := flag.Bool("debug", false, "开启详细调试输出")

	// KCP参数
	sendWindow := flag.Int("sndwnd", 65535, "KCP发送窗口大小")
	recvWindow := flag.Int("rcvwnd", 65535, "KCP接收窗口大小")
	interval := flag.Int("interval", 1, "KCP内部更新时钟间隔(ms)")
	noDelay := flag.Int("nodelay", 1, "是否启用NoDelay模式")
	resend := flag.Int("resend", 2, "快速重传模式")
	noCongestion := flag.Int("nocong", 1, "是否关闭拥塞控制")
	mtu := flag.Int("mtu", 1400, "KCP最大传输单元")

	flag.Parse()

	// 配置测试
	config := TestConfig{
		PacketSize:    *packetSize,
		TestDuration:  *testDuration,
		Parallelism:   *parallelism,
		ReportPeriod:  *reportPeriod,
		IsLatencyTest: *latencyTest,
		Debug:         *debugMode,
	}

	// 配置KCP
	kcpConfig := kcp.KCPConfig{
		NoDelay:      *noDelay,      // NoDelay模式
		Interval:     *interval,     // 内部更新时钟间隔
		Resend:       *resend,       // 快速重传模式
		NoCongestion: *noCongestion, // 是否关闭拥塞控制
		SndWnd:       *sendWindow,   // 发送窗口大小
		RcvWnd:       *recvWindow,   // 接收窗口大小
		MTU:          *mtu,          // 最大传输单元
		StreamMode:   true,          // 流模式
		WriteDelay:   false,         // 不启用写延迟
		AckNoDelay:   true,          // 无延迟ACK
	}

	fmt.Println("10Gbps高性能KCP传输测试工具 v1.0")
	fmt.Println("================================")
	printKCPConfig(kcpConfig)

	// 判断运行模式
	if *serverMode {
		fmt.Printf("服务端模式 [端口: %d]\n", *port)
		if *debugMode {
			fmt.Println("已开启调试模式，将显示详细日志")
		}
		runServer(*port, kcpConfig, *debugMode)
	} else {
		if *targetAddr == "" {
			log.Fatal("必须指定目标服务器地址 (-target)")
		}
		fmt.Printf("客户端模式 [目标: %s]\n", *targetAddr)
		fmt.Printf("测试参数: 包大小=%d字节, 并行=%d流, 持续=%v\n",
			*packetSize, *parallelism, *testDuration)
		if *debugMode {
			fmt.Println("已开启调试模式，将显示详细日志")
		}
		runClient(*targetAddr, config, kcpConfig)
	}
}

// 打印KCP配置信息
func printKCPConfig(config kcp.KCPConfig) {
	fmt.Printf("KCP配置参数：\n")
	fmt.Printf("  NoDelay: %d\n", config.NoDelay)
	fmt.Printf("  Interval: %d ms\n", config.Interval)
	fmt.Printf("  Resend: %d\n", config.Resend)
	fmt.Printf("  NoCongestion: %d\n", config.NoCongestion)
	fmt.Printf("  发送窗口: %d\n", config.SndWnd)
	fmt.Printf("  接收窗口: %d\n", config.RcvWnd)
	fmt.Printf("  MTU: %d\n", config.MTU)
	fmt.Printf("  流模式: %t\n", config.StreamMode)
	fmt.Printf("  写延迟: %t\n", config.WriteDelay)
	fmt.Printf("  无延迟ACK: %t\n", config.AckNoDelay)
}

// 运行服务端
func runServer(port int, kcpConfig kcp.KCPConfig, debug bool) {
	log.Println("性能测试服务端启动...")
	debugLog(debug, "初始化服务端，端口: %d", port)

	// 生成密钥
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	debugLog(debug, "已生成ED25519密钥")

	// 创建多地址
	addr, _ := ma.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/udp/%d/kcp/enc/aes/key/0123456789ABCDEF", port))

	// 创建libp2p主机
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(addr),
		libp2p.DisableRelay(),
		libp2p.NoTransports,
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
		// 使用默认的KCP传输层
		libp2p.Transport(kcp.NewTransport),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 设置KCP全局配置
	kcp.DefaultKCPConfig = kcpConfig

	// 打印服务器信息
	log.Printf("服务端ID: %s", h.ID())
	for _, addr := range h.Addrs() {
		log.Printf("监听地址: %s/p2p/%s", addr, h.ID())
	}

	// 设置吞吐量测试处理程序
	h.SetStreamHandler("/kcp-throughput-test", func(s network.Stream) {
		handleThroughputTest(s)
	})

	// 设置延迟测试处理程序
	h.SetStreamHandler("/kcp-latency-test", func(s network.Stream) {
		handleLatencyTest(s)
	})

	// 等待中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("服务端关闭...")
}

// 处理吞吐量测试流
func handleThroughputTest(s network.Stream) {
	remotePeer := s.Conn().RemotePeer()
	log.Printf("接受来自 %s 的吞吐量测试流", remotePeer)
	kcpLogger.Debugf("KCP连接详情: 远端=%s, 本地地址=%s, 远端地址=%s",
		remotePeer, s.Conn().LocalMultiaddr(), s.Conn().RemoteMultiaddr())

	// 创建一个大缓冲区接收数据
	buffer := make([]byte, 1024*1024) // 1MB缓冲区
	total := int64(0)
	packets := 0

	startTime := time.Now()
	lastReportTime := startTime
	lastBytes := int64(0)

	for {
		n, err := s.Read(buffer)
		if err != nil {
			if err != io.EOF {
				log.Printf("读取错误: %v", err)
				kcpLogger.Debugf("读取流错误: %v, 已读取总字节数: %d", err, total)
			}
			break
		}

		total += int64(n)
		packets++

		// 每秒报告一次吞吐量
		now := time.Now()
		if now.Sub(lastReportTime) >= time.Second {
			bytesPerSecond := float64(total-lastBytes) / now.Sub(lastReportTime).Seconds()
			mbps := bytesPerSecond / 1024 / 1024
			gbps := bytesPerSecond * 8 / 1024 / 1024 / 1024
			log.Printf("当前吞吐量: %.2f MB/s (%.2f Gbps)", mbps, gbps)
			kcpLogger.Debugf("KCP传输状态: 已接收包数=%d, 当前包大小=%d bytes", packets, n)
			lastReportTime = now
			lastBytes = total
		}
	}

	duration := time.Since(startTime)
	throughput := float64(total) / duration.Seconds() / 1024 / 1024
	throughputGbps := throughput * 8 / 1024

	log.Printf("吞吐量测试完成: %.2f MB/s (%.2f Gbps), 总计 %.2f MB, 用时 %.2f 秒",
		throughput, throughputGbps, float64(total)/1024/1024, duration.Seconds())

	// 向客户端发送结果
	resultMsg := fmt.Sprintf("RESULT:%.2f:%d:%d:%.2f",
		throughput, total, packets, duration.Seconds())
	s.Write([]byte(resultMsg))

	s.Close()
}

// 处理延迟测试流
func handleLatencyTest(s network.Stream) {
	log.Printf("接受来自 %s 的延迟测试流", s.Conn().RemotePeer())

	buffer := make([]byte, 1024*1024)
	for {
		n, err := s.Read(buffer)
		if err != nil {
			if err != io.EOF {
				log.Printf("读取错误: %v", err)
			}
			break
		}

		// 立即将数据回传(echo)
		_, err = s.Write(buffer[:n])
		if err != nil {
			log.Printf("写入错误: %v", err)
			break
		}
	}

	s.Close()
}

// 运行客户端
func runClient(targetAddr string, config TestConfig, kcpConfig kcp.KCPConfig) {
	log.Println("性能测试客户端启动...")
	debugLog(config.Debug, "初始化客户端，目标地址: %s", targetAddr)
	ctx := context.Background()

	// 生成密钥
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, rand.Reader)
	if err != nil {
		log.Fatal(err)
	}

	// 创建多地址
	addr, _ := ma.NewMultiaddr("/ip4/0.0.0.0/udp/0/kcp/enc/aes/key/0123456789ABCDEF")

	// 设置KCP全局配置
	kcp.DefaultKCPConfig = kcpConfig

	// 创建libp2p主机
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(addr),
		libp2p.DisableRelay(),
		libp2p.NoTransports,
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
		// 使用默认的KCP传输层
		libp2p.Transport(kcp.NewTransport),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 连接到服务器
	maddr, err := ma.NewMultiaddr(targetAddr)
	if err != nil {
		log.Fatalf("无效地址: %v", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		log.Fatalf("无法解析节点信息: %v", err)
	}

	h.Peerstore().AddAddrs(peerInfo.ID, peerInfo.Addrs, peerstore.PermanentAddrTTL)
	log.Printf("连接到节点 %s", peerInfo.ID)

	if err := h.Connect(ctx, *peerInfo); err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	log.Println("连接已建立")

	// 根据测试类型运行测试
	if config.IsLatencyTest {
		result := runLatencyTest(ctx, h, peerInfo.ID, config)
		printLatencyResult(result)
	} else {
		result := runThroughputTest(ctx, h, peerInfo.ID, config)
		printThroughputResult(result)
	}

	// 关闭主机
	h.Close()
}

// 运行吞吐量测试
func runThroughputTest(ctx context.Context, h host.Host, peerID peer.ID, config TestConfig) PerfResult {
	log.Printf("开始吞吐量测试: 包大小=%d bytes, 持续时间=%v, 并行数=%d",
		config.PacketSize, config.TestDuration, config.Parallelism)
	debugLog(config.Debug, "KCP连接信息: 本机ID=%s, 远端ID=%s", h.ID(), peerID)

	var wg sync.WaitGroup
	results := make(chan PerfResult, config.Parallelism)

	for i := 0; i < config.Parallelism; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// 打开一个新流
			debugLog(config.Debug, "流 #%d: 开始创建新KCP流", id)
			s, err := h.NewStream(ctx, peerID, "/kcp-throughput-test")
			if err != nil {
				log.Printf("无法打开流 #%d: %v", id, err)
				return
			}
			defer s.Close()
			debugLog(config.Debug, "流 #%d: 成功建立KCP流连接", id)

			// 准备测试数据包
			packet := make([]byte, config.PacketSize)
			rand.Read(packet)
			debugLog(config.Debug, "流 #%d: 已生成 %d 字节的测试数据包", id, len(packet))

			// 开始发送
			startTime := time.Now()
			deadline := startTime.Add(config.TestDuration)

			var total int64
			var packets int
			lastReportTime := startTime
			lastBytes := int64(0)

			// 连续发送循环
			for time.Now().Before(deadline) {
				// 连续发送多个数据包以提高吞吐量
				for j := 0; j < 100 && time.Now().Before(deadline); j++ {
					n, err := s.Write(packet)
					if err != nil {
						log.Printf("写入错误: %v", err)
						debugLog(config.Debug, "流 #%d: 写入错误: %v", id, err)
						goto endLoop // 使用goto跳出嵌套循环
					}

					total += int64(n)
					packets++
				}

				// 周期性报告
				now := time.Now()
				if now.Sub(lastReportTime) >= config.ReportPeriod {
					bytesPerSecond := float64(total-lastBytes) / now.Sub(lastReportTime).Seconds()
					mbps := bytesPerSecond / 1024 / 1024
					gbps := bytesPerSecond * 8 / 1024 / 1024 / 1024
					log.Printf("流 #%d - 当前吞吐量: %.2f MB/s (%.2f Gbps)", id, mbps, gbps)
					debugLog(config.Debug, "流 #%d: 已发送 %d 个数据包, 总计 %.2f MB",
						id, packets, float64(total)/1024/1024)
					lastReportTime = now
					lastBytes = total
				}
			}

		endLoop:
			duration := time.Since(startTime)
			mbps := float64(total) / duration.Seconds() / 1024 / 1024
			gbps := mbps * 8 / 1024
			log.Printf("流 #%d 完成发送: 总计 %.2f MB, 用时 %.2f 秒, 吞吐量 %.2f MB/s (%.2f Gbps)",
				id, float64(total)/1024/1024, duration.Seconds(), mbps, gbps)
			debugLog(config.Debug, "流 #%d: 测试完成, 等待服务器响应", id)

			// 读取服务器返回的测试结果
			buffer := make([]byte, 256)
			n, err := s.Read(buffer)
			if err != nil && err != io.EOF {
				log.Printf("读取结果错误: %v", err)
				debugLog(config.Debug, "流 #%d: 读取结果失败: %v", id, err)
			}

			resultStr := string(buffer[:n])
			log.Printf("服务器结果: %s", resultStr)
			debugLog(config.Debug, "流 #%d: 收到服务器结果: %s", id, resultStr)

			// 计算本地结果
			throughput := float64(total) / duration.Seconds() / 1024 / 1024

			results <- PerfResult{
				Throughput:   throughput,
				TotalBytes:   total,
				TotalPackets: packets,
				Duration:     duration,
				Parallelism:  config.Parallelism,
			}
		}(i)
	}

	// 等待所有测试完成
	wg.Wait()
	close(results)

	// 汇总结果
	var totalResult PerfResult
	count := 0

	for result := range results {
		totalResult.Throughput += result.Throughput
		totalResult.TotalBytes += result.TotalBytes
		totalResult.TotalPackets += result.TotalPackets
		if count == 0 || result.Duration > totalResult.Duration {
			totalResult.Duration = result.Duration
		}
		count++
	}

	totalResult.Parallelism = config.Parallelism
	return totalResult
}

// 运行延迟测试
func runLatencyTest(ctx context.Context, h host.Host, peerID peer.ID, config TestConfig) PerfResult {
	log.Printf("开始延迟测试: 包大小=%d bytes, 持续时间=%v",
		config.PacketSize, config.TestDuration)

	// 打开一个新流
	s, err := h.NewStream(ctx, peerID, "/kcp-latency-test")
	if err != nil {
		log.Fatalf("无法打开流: %v", err)
	}
	defer s.Close()

	// 准备测试数据包
	packet := make([]byte, config.PacketSize)
	rand.Read(packet)

	// 接收缓冲区
	recvBuf := make([]byte, config.PacketSize)

	// 开始测试
	startTime := time.Now()
	deadline := startTime.Add(config.TestDuration)

	var minLatency = time.Hour
	var maxLatency time.Duration
	var totalLatency time.Duration
	var packetCount int
	var lostPackets int

	for time.Now().Before(deadline) {
		sendTime := time.Now()

		// 发送数据包
		_, err := s.Write(packet)
		if err != nil {
			log.Printf("写入错误: %v", err)
			lostPackets++
			continue
		}

		// 设置读取超时
		s.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

		// 接收回显
		_, err = io.ReadFull(s, recvBuf)
		recvTime := time.Now()

		if err != nil {
			if err, ok := err.(interface{ Timeout() bool }); ok && err.Timeout() {
				log.Println("读取超时")
				lostPackets++
			} else {
				log.Printf("读取错误: %v", err)
				lostPackets++
			}
			continue
		}

		// 计算单次延迟
		latency := recvTime.Sub(sendTime)
		totalLatency += latency
		packetCount++

		if latency < minLatency {
			minLatency = latency
		}
		if latency > maxLatency {
			maxLatency = latency
		}

		// 定期报告
		if packetCount%100 == 0 {
			log.Printf("当前RTT: %.2f ms, 平均: %.2f ms, 最小: %.2f ms, 最大: %.2f ms",
				float64(latency.Microseconds())/1000,
				float64(totalLatency.Microseconds())/float64(packetCount)/1000,
				float64(minLatency.Microseconds())/1000,
				float64(maxLatency.Microseconds())/1000)
		}

		// 短暂休息，避免过载
		time.Sleep(10 * time.Millisecond)
	}

	// 计算最终结果
	avgLatency := float64(totalLatency.Microseconds()) / float64(packetCount) / 1000
	packetLoss := float64(lostPackets) / float64(packetCount+lostPackets) * 100

	return PerfResult{
		AvgLatency:   avgLatency,
		MinLatency:   float64(minLatency.Microseconds()) / 1000,
		MaxLatency:   float64(maxLatency.Microseconds()) / 1000,
		PacketLoss:   packetLoss,
		TotalPackets: packetCount + lostPackets,
		Duration:     time.Since(startTime),
		Parallelism:  config.Parallelism,
	}
}

// 打印吞吐量结果
func printThroughputResult(result PerfResult) {
	fmt.Println("\n========== 吞吐量测试结果 ==========")
	fmt.Printf("总吞吐量:    %.2f MB/s (%.2f GB/s)\n", result.Throughput, result.Throughput/1024)
	fmt.Printf("网络速率:    %.2f Mbps (%.2f Gbps)\n", result.Throughput*8, result.Throughput*8/1024)
	fmt.Printf("总传输数据:  %.2f MB (%.2f GB)\n", float64(result.TotalBytes)/1024/1024, float64(result.TotalBytes)/1024/1024/1024)
	fmt.Printf("总数据包:    %d\n", result.TotalPackets)
	fmt.Printf("平均包大小:  %.2f bytes\n", float64(result.TotalBytes)/float64(result.TotalPackets))
	fmt.Printf("包传输速率:  %.2f 包/秒\n", float64(result.TotalPackets)/result.Duration.Seconds())
	fmt.Printf("测试时长:    %.2f 秒\n", result.Duration.Seconds())
	fmt.Printf("并行流数量:  %d\n", result.Parallelism)
	fmt.Println("===================================")
}

// 打印延迟结果
func printLatencyResult(result PerfResult) {
	fmt.Println("\n========== 延迟测试结果 ==========")
	fmt.Printf("平均延迟(RTT): %.2f ms\n", result.AvgLatency)
	fmt.Printf("最小延迟:      %.2f ms\n", result.MinLatency)
	fmt.Printf("最大延迟:      %.2f ms\n", result.MaxLatency)
	fmt.Printf("丢包率:        %.2f%%\n", result.PacketLoss)
	fmt.Printf("总发送包数:    %d\n", result.TotalPackets)
	fmt.Printf("测试时长:      %.2f 秒\n", result.Duration.Seconds())
	fmt.Printf("并行流数量:    %d\n", result.Parallelism)
	fmt.Println("=================================")
}
