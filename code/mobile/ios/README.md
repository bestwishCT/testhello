# iOS 构建说明

## 环境要求
- Go 1.21 或更高版本
- gomobile 和 gobind
- Xcode 和 iOS SDK
- Windows 或 macOS 系统

## 目录结构
```
code/
  ├── mobile/
  │   ├── android/        # Android 相关代码
  │   ├── ios/           # iOS 相关代码
  │   │   ├── bind_ios.go    # iOS 绑定代码
  │   │   ├── build.bat      # Windows 构建脚本
  │   │   └── README.md      # 本文档
  │   └── mobile.go      # 共享的移动端代码
  └── ...
```

## 构建步骤

### Windows 环境
1. 安装 Go 和 gomobile：
```bash
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
gomobile init
```

2. 运行构建脚本：
```bash
cd code/mobile/ios
build.bat
```

### macOS 环境
1. 安装 Go 和 gomobile：
```bash
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
gomobile init
```

2. 运行构建脚本：
```bash
cd code/mobile/ios
./build.sh
```

## 输出文件
构建成功后，将在 `ios` 目录下生成 `ShileP2P.framework` 文件。

## 使用说明

### 1. 在 Xcode 项目中集成框架

1. 将 `ShileP2P.framework` 拖入 Xcode 项目
2. 在项目设置中确保框架已添加到 "Embedded Binaries" 和 "Linked Frameworks and Libraries"

### 2. 基本使用示例

```swift
import ShileP2P

class P2PManager {
    private var client: ShileP2PMobileClient?
    
    func initialize() {
        // 创建客户端实例
        client = ShileP2PNewMobileClient("127.0.0.1:8001", "127.0.0.1:8002")
    }
    
    func start() {
        do {
            try client?.start()
            print("P2P client started successfully")
        } catch {
            print("Failed to start P2P client: \(error)")
        }
    }
    
    func stop() {
        do {
            try client?.stop()
            print("P2P client stopped successfully")
        } catch {
            print("Failed to stop P2P client: \(error)")
        }
    }
}
```

### 3. 高级使用示例

```swift
class P2PManager {
    private var client: ShileP2PMobileClient?
    private var speedTestTimer: Timer?
    
    // 初始化并启动客户端
    func setupAndStart() {
        client = ShileP2PNewMobileClient("127.0.0.1:8001", "127.0.0.1:8002")
        
        do {
            try client?.start()
            print("Client started")
            
            // 启动速度测试
            startSpeedTest()
            
            // 获取已连接节点
            if let agents = try client?.getConnectedAgents() {
                print("Connected agents: \(agents)")
            }
        } catch {
            print("Error: \(error)")
        }
    }
    
    // 启动速度测试
    func startSpeedTest() {
        do {
            try client?.runSpeedTest()
            print("Speed test started")
            
            // 设置定时器检查速度测试结果
            speedTestTimer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
                self?.checkSpeedTestResults()
            }
        } catch {
            print("Failed to start speed test: \(error)")
        }
    }
    
    // 检查速度测试结果
    private func checkSpeedTestResults() {
        // 这里可以添加获取速度测试结果的逻辑
    }
    
    // 停止速度测试
    func stopSpeedTest() {
        do {
            try client?.stopSpeedTest()
            speedTestTimer?.invalidate()
            speedTestTimer = nil
            print("Speed test stopped")
        } catch {
            print("Failed to stop speed test: \(error)")
        }
    }
    
    // 清理资源
    func cleanup() {
        stopSpeedTest()
        stop()
        client = nil
    }
}
```

### 4. 错误处理示例

```swift
enum P2PError: Error {
    case clientNotInitialized
    case startFailed(String)
    case stopFailed(String)
    case speedTestFailed(String)
}

class P2PManager {
    private var client: ShileP2PMobileClient?
    
    func startWithErrorHandling() throws {
        guard let client = client else {
            throw P2PError.clientNotInitialized
        }
        
        do {
            try client.start()
        } catch {
            throw P2PError.startFailed(error.localizedDescription)
        }
    }
    
    func runSpeedTestWithErrorHandling() throws {
        guard let client = client else {
            throw P2PError.clientNotInitialized
        }
        
        do {
            try client.runSpeedTest()
        } catch {
            throw P2PError.speedTestFailed(error.localizedDescription)
        }
    }
}
```

## 注意事项

1. 网络权限
   - 在 Info.plist 中添加必要的网络权限
   - 确保应用有适当的网络访问权限

2. 后台运行
   - 如需后台运行，需要在 Info.plist 中配置相应的后台模式
   - 实现适当的后台任务处理

3. 错误处理
   - 所有 P2P 操作都应该进行适当的错误处理
   - 实现重试机制和错误恢复策略

4. 性能优化
   - 避免在主线程进行耗时操作
   - 使用适当的线程管理
   - 实现资源释放机制

5. 调试
   - 使用 Xcode 的调试工具监控网络活动
   - 实现日志记录机制
   - 使用 Instruments 进行性能分析

## 常见问题

1. 框架加载失败
   - 检查框架是否正确添加到项目中
   - 验证框架的架构是否与项目匹配
   - 确保框架已正确签名

2. 网络连接问题
   - 检查网络权限设置
   - 验证网络配置是否正确
   - 检查防火墙设置

3. 性能问题
   - 使用 Instruments 进行性能分析
   - 检查内存使用情况
   - 优化网络请求

4. 构建问题
   - 确保 Go 和 gomobile 版本正确
   - 检查构建脚本的执行权限
   - 验证环境变量设置 