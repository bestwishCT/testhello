# ShiledP2P Android 客户端

这是 ShiledP2P 的 Android 客户端实现，使用 gomobile 构建。该客户端提供了完整的 P2P 连接功能，包括：

- P2P 连接建立
- NAT 打洞
- 节点发现
- 网络测速
- 代理转发

## 环境要求

- Android SDK 21+
- Android NDK r21+
- Go 1.16+
- gomobile

## 构建步骤

1. 安装 gomobile：
```bash
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
```

2. 初始化 gomobile：
```bash
gomobile init
```

3. 构建 Android 库：
```bash
./build.sh
```

构建完成后，会在 `build` 目录下生成 `shiledp2p.aar` 文件。

## 使用方法

1. 在 Android 项目中添加依赖：
```gradle
dependencies {
    implementation files('libs/shiledp2p.aar')
}
```

2. 初始化客户端：
```java
MobileClient client = new MobileClient(
    "/ip4/0.0.0.0/udp/0/quic-v1",
    "/ip4/0.0.0.0/udp/0/kcp"
);
```

3. 设置回调函数：
```java
// 状态回调
client.setStatusCallback(new MobileClient.StatusCallback() {
    @Override
    public void onStatus(String status) {
        // 处理状态更新
    }
});

// 错误回调
client.setErrorCallback(new MobileClient.ErrorCallback() {
    @Override
    public void onError(String err) {
        // 处理错误
    }
});

// Agent列表回调
client.setAgentListCallback(new MobileClient.AgentListCallback() {
    @Override
    public void onAgentList(String[] agents) {
        // 处理Agent列表更新
    }
});

// 测速结果回调
client.setSpeedTestCallback(new MobileClient.SpeedTestCallback() {
    @Override
    public void onSpeedTest(String result) {
        // 处理测速结果
    }
});
```

4. 启动客户端：
```java
boolean success = client.start(8080, 8081);
```

5. 获取Agent列表：
```java
client.bootstrap();
```

6. 运行测速：
```java
client.runSpeedTest(peerID, fileSize, duration);
```

7. 停止客户端：
```java
client.stop();
```

## 注意事项

1. 在 AndroidManifest.xml 中添加必要的权限：
```xml
<uses-permission android:name="android.permission.INTERNET" />
<uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />
<uses-permission android:name="android.permission.ACCESS_WIFI_STATE" />
```

2. 资源管理：
- 在 Activity 销毁时调用 `client.stop()`
- 确保所有回调都在主线程中更新 UI

3. 常见问题：
- 如果遇到权限问题，请检查 AndroidManifest.xml 中的权限设置
- 如果遇到网络问题，请确保设备有网络连接
- 如果遇到崩溃，请检查是否在主线程中更新 UI

## 示例代码

完整的示例代码请参考 `MainActivity.java` 和 `activity_main.xml`。

## 贡献

欢迎提交 Issue 和 Pull Request。

## 许可证

MIT License 