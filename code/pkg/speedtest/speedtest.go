package speedtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"shiledp2p/pkg/common"
)

var logger *log.Logger

func init() {
	// 创建日志记录器，同时输出到标准输出和文件
	logFile, err := os.OpenFile("speedtest.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		multiWriter := io.MultiWriter(os.Stdout, logFile)
		logger = log.New(multiWriter, "[测速] ", log.LstdFlags)
	} else {
		// 如果无法创建文件，只输出到标准输出
		logger = log.New(os.Stdout, "[测速] ", log.LstdFlags)
	}
}

// 用于替代直接使用fmt.Printf的日志函数
func logf(format string, args ...interface{}) {
	logger.Printf(format, args...)
}

// 测速请求消息结构
type SpeedTestRequest struct {
	Type       string `json:"type"`        // "start", "stop"
	TestID     string `json:"test_id"`     // 唯一测试ID
	FileSize   int64  `json:"file_size"`   // 要测试的文件大小，单位字节
	BufferSize int    `json:"buffer_size"` // 缓冲区大小
	Duration   int    `json:"duration"`    // 测试持续时间（秒）
}

// 测速结果消息结构
type SpeedTestResult struct {
	TestID         string  `json:"test_id"`         // 唯一测试ID
	TotalBytes     int64   `json:"total_bytes"`     // 总传输字节数
	Duration       float64 `json:"duration"`        // 实际持续时间（秒）
	Throughput     float64 `json:"throughput"`      // 吞吐量（MB/s）
	MbpsThroughput float64 `json:"mbps_throughput"` // 吞吐量（Mbps）
}

// SpeedTest 测速管理器
type SpeedTest struct {
	host        host.Host
	ctx         context.Context
	cancel      context.CancelFunc
	activeMu    sync.RWMutex
	activeTests map[string]context.CancelFunc // 活跃测试
}

// NewSpeedTest 创建新的测速管理器
func NewSpeedTest(h host.Host) *SpeedTest {
	ctx, cancel := context.WithCancel(context.Background())
	return &SpeedTest{
		host:        h,
		ctx:         ctx,
		cancel:      cancel,
		activeTests: make(map[string]context.CancelFunc),
	}
}

// Start 启动测速服务
func (s *SpeedTest) Start() {
	// 注册处理函数
	common.RegisterStreamHandler(s.host, common.SpeedTestProtocolID, s.handleSpeedTest)
	logf("启动完成")
}

// Stop 停止测速服务
func (s *SpeedTest) Stop() {
	// 取消所有活跃测试
	s.activeMu.Lock()
	for testID, cancelFunc := range s.activeTests {
		cancelFunc()
		logf("取消测试 %s", testID)
	}
	s.activeTests = make(map[string]context.CancelFunc)
	s.activeMu.Unlock()

	// 取消主上下文
	s.cancel()
	logf("已停止")
}

// handleSpeedTest 处理测速请求
func (s *SpeedTest) handleSpeedTest(stream network.Stream) {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer()
	logf("收到来自 %s 的测速请求", remotePeer.String())

	// 读取请求
	buf := make([]byte, 2048)
	n, err := stream.Read(buf)
	if err != nil && err != io.EOF {
		logf("读取测速请求失败: %v", err)
		return
	}

	// 解析请求
	var request SpeedTestRequest
	if err := json.Unmarshal(buf[:n], &request); err != nil {
		logf("解析测速请求失败: %v", err)
		return
	}

	logf("收到测试请求: ID=%s, 大小=%d字节, 缓冲区=%d, 持续=%d秒",
		request.TestID, request.FileSize, request.BufferSize, request.Duration)

	// 根据请求类型处理
	switch request.Type {
	case "start":
		// 创建测试上下文
		testCtx, cancel := context.WithCancel(s.ctx)

		// 存储活跃测试
		s.activeMu.Lock()
		s.activeTests[request.TestID] = cancel
		s.activeMu.Unlock()

		// 设置超时
		var duration time.Duration
		if request.Duration > 0 {
			duration = time.Duration(request.Duration) * time.Second
		} else {
			duration = 30 * time.Second // 默认30秒
		}
		timeoutCtx, timeoutCancel := context.WithTimeout(testCtx, duration)
		defer timeoutCancel()

		// 创建响应流
		respStream, err := s.host.NewStream(timeoutCtx, remotePeer, common.SpeedTestProtocolID)
		if err != nil {
			logf("创建响应流失败: %v", err)
			return
		}
		defer respStream.Close()

		// 确认请求接收
		respRequest := SpeedTestRequest{
			Type:   "start_ack",
			TestID: request.TestID,
		}
		respData, _ := json.Marshal(respRequest)
		if _, err := stream.Write(respData); err != nil {
			logf("发送确认失败: %v", err)
			return
		}

		// 开始发送数据
		startTime := time.Now()

		// 创建缓冲区
		bufSize := request.BufferSize
		if bufSize <= 0 {
			bufSize = 64 * 1024 // 默认64KB
		}
		dataBuffer := make([]byte, bufSize)

		// 随机填充数据
		_, err = rand.Read(dataBuffer)
		if err != nil {
			logf("生成随机数据失败: %v", err)
			return
		}

		// 发送数据循环
		var totalSent int64
		var lastReportTime = startTime
		var lastReportBytes int64

		for {
			select {
			case <-timeoutCtx.Done():
				// 时间到或上下文取消
				goto SEND_COMPLETE
			default:
				// 发送数据
				n, err := respStream.Write(dataBuffer)
				if err != nil {
					logf("发送数据失败: %v", err)
					goto SEND_COMPLETE
				}
				totalSent += int64(n)

				// 定期报告进度
				now := time.Now()
				if now.Sub(lastReportTime) >= time.Second {
					bytePerSec := float64(totalSent-lastReportBytes) / now.Sub(lastReportTime).Seconds()
					mbps := bytePerSec * 8 / 1024 / 1024
					logf("测试 %s 进度: 已发送 %.2f MB, 当前速率 %.2f Mbps",
						request.TestID, float64(totalSent)/1024/1024, mbps)
					lastReportTime = now
					lastReportBytes = totalSent
				}
			}
		}

	SEND_COMPLETE:
		// 计算结果
		var testDuration time.Duration
		testDuration = time.Since(startTime)
		throughput := float64(totalSent) / testDuration.Seconds() / 1024 / 1024 // MB/s
		mbpsThroughput := throughput * 8                                        // Mbps

		// 发送结果
		result := SpeedTestResult{
			TestID:         request.TestID,
			TotalBytes:     totalSent,
			Duration:       testDuration.Seconds(),
			Throughput:     throughput,
			MbpsThroughput: mbpsThroughput,
		}

		resultData, _ := json.Marshal(result)
		if _, err := stream.Write(resultData); err != nil {
			logf("发送结果失败: %v", err)
		}

		logf("测试 %s 完成: 发送 %.2f MB, 用时 %.2f 秒, 吞吐量 %.2f MB/s (%.2f Mbps)",
			request.TestID, float64(totalSent)/1024/1024, testDuration.Seconds(), throughput, mbpsThroughput)

		// 清理活跃测试
		s.activeMu.Lock()
		delete(s.activeTests, request.TestID)
		s.activeMu.Unlock()

	case "stop":
		// 停止测试
		s.activeMu.RLock()
		cancelFunc, exists := s.activeTests[request.TestID]
		s.activeMu.RUnlock()

		if exists {
			cancelFunc()
			logf("测试 %s 已停止", request.TestID)

			// 清理活跃测试
			s.activeMu.Lock()
			delete(s.activeTests, request.TestID)
			s.activeMu.Unlock()
		}

		// 发送停止确认
		response := SpeedTestRequest{
			Type:   "stop_ack",
			TestID: request.TestID,
		}
		responseData, _ := json.Marshal(response)
		stream.Write(responseData)
	}
}

// RunSpeedTest 向对等节点发起测速测试
func (s *SpeedTest) RunSpeedTest(ctx context.Context, targetPeerID peer.ID, fileSize int64, bufferSize int, duration int) (*SpeedTestResult, error) {
	if fileSize <= 0 {
		fileSize = 100 * 1024 * 1024 // 默认100MB
	}
	if bufferSize <= 0 {
		bufferSize = 64 * 1024 // 默认64KB
	}
	if duration <= 0 {
		duration = 30 // 默认30秒
	}

	// 生成唯一测试ID
	testID := fmt.Sprintf("speedtest_%d", time.Now().UnixNano())

	logf("开始向节点 %s 发起测速请求: ID=%s, 大小=%d MB, 缓冲区=%d KB, 持续=%d秒",
		targetPeerID.String(), testID, fileSize/1024/1024, bufferSize/1024, duration)

	// 创建流
	stream, err := s.host.NewStream(ctx, targetPeerID, common.SpeedTestProtocolID)
	if err != nil {
		return nil, fmt.Errorf("创建测速流失败: %w", err)
	}
	defer stream.Close()

	// 创建请求
	request := SpeedTestRequest{
		Type:       "start",
		TestID:     testID,
		FileSize:   fileSize,
		BufferSize: bufferSize,
		Duration:   duration,
	}

	// 发送请求
	requestData, _ := json.Marshal(request)
	if _, err := stream.Write(requestData); err != nil {
		return nil, fmt.Errorf("发送测速请求失败: %w", err)
	}

	// 读取确认
	buf := make([]byte, 2048)
	n, err := stream.Read(buf)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("读取确认失败: %w", err)
	}

	var response SpeedTestRequest
	if err := json.Unmarshal(buf[:n], &response); err != nil {
		return nil, fmt.Errorf("解析确认失败: %w", err)
	}

	if response.Type != "start_ack" || response.TestID != testID {
		return nil, fmt.Errorf("收到无效确认")
	}

	logf("测试 %s 已确认开始，等待接收数据...", testID)

	// 开始接收数据
	startTime := time.Now()
	var totalReceived int64
	var lastReportTime = startTime
	var lastReportBytes int64

	// 使用更大的缓冲区接收数据
	recvBuffer := make([]byte, bufferSize)

	// 设置测试超时
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(duration+10)*time.Second)
	defer cancel()

	// 监听目标流获取数据
	recvStream := make(chan network.Stream, 1)
	errCh := make(chan error, 1)

	go func() {
		// 等待对方发起数据流
		recvHandler := func(s network.Stream) {
			remotePeer := s.Conn().RemotePeer()
			if remotePeer == targetPeerID {
				recvStream <- s
			} else {
				s.Close()
			}
		}

		// 临时注册处理函数
		common.RegisterStreamHandler(s.host, common.SpeedTestProtocolID, recvHandler)

		// 确保清理处理函数
		defer func() {
			common.RegisterStreamHandler(s.host, common.SpeedTestProtocolID, s.handleSpeedTest)
		}()

		// 等待超时
		<-timeoutCtx.Done()
		errCh <- fmt.Errorf("等待数据流超时")
	}()

	// 等待数据流或超时
	var dataStream network.Stream
	select {
	case dataStream = <-recvStream:
		logf("收到测试 %s 的数据流", testID)
	case err := <-errCh:
		return nil, err
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("等待数据流超时")
	}
	defer dataStream.Close()

	// 循环接收数据
	for {
		select {
		case <-timeoutCtx.Done():
			// 已达到时间限制
			goto RECEIVE_COMPLETE
		default:
			// 接收数据
			n, err := dataStream.Read(recvBuffer)
			if err != nil {
				if err != io.EOF {
					logf("接收数据错误: %v", err)
				}
				goto RECEIVE_COMPLETE
			}

			totalReceived += int64(n)

			// 定期报告进度
			now := time.Now()
			if now.Sub(lastReportTime) >= time.Second {
				bytePerSec := float64(totalReceived-lastReportBytes) / now.Sub(lastReportTime).Seconds()
				mbps := bytePerSec * 8 / 1024 / 1024
				logf("测试 %s 进度: 已接收 %.2f MB, 当前速率 %.2f Mbps",
					testID, float64(totalReceived)/1024/1024, mbps)
				lastReportTime = now
				lastReportBytes = totalReceived
			}
		}
	}

RECEIVE_COMPLETE:
	// 计算结果
	elapsedDuration := time.Since(startTime)
	throughput := float64(totalReceived) / elapsedDuration.Seconds() / 1024 / 1024 // MB/s
	mbpsThroughput := throughput * 8                                               // Mbps

	// 等待对方的结果
	n, err = stream.Read(buf)
	if err != nil && err != io.EOF {
		logf("等待结果时发生错误: %v", err)
		// 使用本地计算的结果继续
	}

	var serverResult SpeedTestResult
	if err == nil || err == io.EOF {
		if err := json.Unmarshal(buf[:n], &serverResult); err != nil {
			logf("解析服务器结果失败: %v，使用本地结果", err)
			// 使用本地计算的结果继续
		} else {
			logf("收到服务器端结果: 发送 %.2f MB, 吞吐量 %.2f MB/s (%.2f Mbps)",
				float64(serverResult.TotalBytes)/1024/1024, serverResult.Throughput, serverResult.MbpsThroughput)
		}
	}

	// 生成最终结果
	result := &SpeedTestResult{
		TestID:         testID,
		TotalBytes:     totalReceived,
		Duration:       elapsedDuration.Seconds(),
		Throughput:     throughput,
		MbpsThroughput: mbpsThroughput,
	}

	logf("测试 %s 完成: 接收 %.2f MB, 用时 %.2f 秒, 吞吐量 %.2f MB/s (%.2f Mbps)",
		testID, float64(totalReceived)/1024/1024, elapsedDuration.Seconds(), throughput, mbpsThroughput)

	return result, nil
}
