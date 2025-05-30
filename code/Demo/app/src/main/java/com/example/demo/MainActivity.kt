package com.example.demo

import android.Manifest
import android.annotation.SuppressLint
import android.content.pm.PackageManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.view.ViewGroup
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.core.view.marginTop
import androidx.core.view.setPadding
import androidx.webkit.ProxyConfig
import androidx.webkit.ProxyController
import com.example.demo.ui.theme.DemoTheme
import mobile.MobileClient
import org.json.JSONArray
import java.util.*


class MainActivity : ComponentActivity() {

    private var client: MobileClient? = null
    private val mainHandler = Handler(Looper.getMainLooper())
    private val logMessages = mutableStateListOf<String>()
    private val currentStatus = mutableStateOf("未启动")
    private val isClientRunning = mutableStateOf(false)
    private var webView: WebView? = null

    private val requestPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions()
    ) { permissions ->
        val allGranted = permissions.entries.all { it.value }
        if (allGranted) {
            initP2PClient()
        } else {
            updateStatus("缺少必要的网络权限，无法启动P2P客户端")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        
        // 启用WebView调试
        WebView.setWebContentsDebuggingEnabled(true)
        
        setContent {
            DemoTheme {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background
                ) {
                    MainScreen(
                        status = currentStatus.value,
                        logs = logMessages,
                        isRunning = isClientRunning.value,
                        onStartClick = { startClient() },
                        onStopClick = { stopClient() },
                        onBootstrapClick = { bootstrap() },
                        onSpeedTestClick = { runSpeedTest() },
                        onStopSpeedTestClick = { stopSpeedTest() },
                        onTestHelloClick = { testHello() }
                    )
                }
            }
        }
        checkAndRequestPermissions()
        
        // 在应用启动时测试一次
        appendLog("应用启动，测试未启动客户端时的Google访问...")
        testGoogleAccess()
    }

    @SuppressLint("RequiresFeature")
    private fun setProxy() {
        try {
            val proxyUrl = "http://127.0.0.1:58080"
            appendLog("正在设置WebView代理...")
            appendLog("代理地址: $proxyUrl")
            
            // 设置系统代理
            System.setProperty("http.proxyHost", "127.0.0.1")
            System.setProperty("http.proxyPort", "58080")
            System.setProperty("https.proxyHost", "127.0.0.1")
            System.setProperty("https.proxyPort", "58080")
            
            // 设置WebView代理
            val proxyConfig: ProxyConfig = ProxyConfig.Builder()
                .addProxyRule(proxyUrl)
                .setReverseBypassEnabled(true)
                .build()
            
            ProxyController.getInstance().setProxyOverride(proxyConfig, {
                appendLog("WebView代理设置成功")
                // 执行代理连接测试
                testProxyConnection()
            }) {
                appendLog("WebView代理设置失败，请检查系统权限和代理配置")
            }
        } catch (e: Exception) {
            appendLog("设置WebView代理时出错: ${e.message}")
            appendLog("详细错误信息: ${e.toString()}")
            e.printStackTrace()
        }
    }

    private fun testGoogleAccess() {
        Thread {
            try {
                appendLog("\n开始测试访问Google...")
                appendLog("当前客户端状态: ${if (isClientRunning.value) "已启动" else "未启动"}")
                
                val url = java.net.URL("https://www.google.com")
                val connection = url.openConnection() as java.net.HttpURLConnection
                connection.connectTimeout = 10000
                connection.readTimeout = 10000
                connection.requestMethod = "GET"
                connection.setRequestProperty("User-Agent", "Mozilla/5.0")
                
                try {
                    appendLog("正在尝试连接...")
                    val responseCode = connection.responseCode
                    appendLog("响应码: $responseCode")
                    
                    if (responseCode == 200) {
                        appendLog("成功访问Google!")
                        val content = connection.inputStream.bufferedReader().use { it.readText() }
                        appendLog("响应内容长度: ${content.length}")
                    } else {
                        appendLog("访问Google失败，响应码: $responseCode")
                        try {
                            val errorContent = connection.errorStream?.bufferedReader()?.use { it.readText() }
                            if (errorContent != null) {
                                appendLog("错误响应内容: $errorContent")
                            }
                        } catch (e: Exception) {
                            appendLog("无法读取错误响应: ${e.message}")
                        }
                    }
                } catch (e: Exception) {
                    appendLog("连接测试出错: ${e.message}")
                    appendLog("详细错误信息: ${e.toString()}")
                    if (e.message?.contains("timeout", ignoreCase = true) == true) {
                        appendLog("连接超时，这是预期的结果（未启动客户端时）")
                    }
                } finally {
                    connection.disconnect()
                }
            } catch (e: Exception) {
                appendLog("测试访问Google时出错: ${e.message}")
            }
        }.start()
    }

    private fun testProxyConnection() {
        Thread {
            try {
                appendLog("开始代理连接测试...")
                
                // 首先检查Agent连接状态
                val agentsJson = client?.getConnectedAgents()
                appendLog("当前连接的Agent列表: $agentsJson")
                
                if (agentsJson.isNullOrEmpty() || agentsJson == "[]") {
                    appendLog("警告: 没有连接到任何Agent，代理可能无法工作")
                    appendLog("建议先点击'获取Agent列表'按钮")
                    return@Thread
                }
                
                // 检查代理服务器状态
                try {
                    appendLog("正在检查代理服务器端口 58080...")
                    val proxySocket = java.net.Socket()
                    proxySocket.connect(java.net.InetSocketAddress("127.0.0.1", 58080), 10000)
                    appendLog("代理服务器端口 58080 正在监听")
                    proxySocket.close()
                } catch (e: Exception) {
                    appendLog("代理服务器端口 58080 未响应: ${e.message}")
                    appendLog("请确认 Go mobile 端代理服务器是否正常启动")
                    return@Thread
                }

                // 测试访问Google
                testGoogleAccess()
            } catch (e: Exception) {
                appendLog("代理测试过程出错: ${e.message}")
                appendLog("详细错误信息: ${e.toString()}")
                e.printStackTrace()
            }
        }.start()
    }

    private fun checkAndRequestPermissions() {
        val permissions = arrayOf(
            Manifest.permission.INTERNET,
            Manifest.permission.ACCESS_NETWORK_STATE,
            Manifest.permission.ACCESS_WIFI_STATE
        )

        val permissionsToRequest = permissions.filter {
            ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED
        }.toTypedArray()

        if (permissionsToRequest.isEmpty()) {
            initP2PClient()
        } else {
            requestPermissionLauncher.launch(permissionsToRequest)
        }
    }

    private fun initP2PClient() {
        Thread {
            try {
                client = MobileClient(
                    "/ip4/0.0.0.0/udp/0/quic-v1",
                    "/ip4/0.0.0.0/udp/0/kcp"
                )
//                setupCallbacks()
                updateStatus("P2P客户端初始化成功")
            } catch (e: Exception) {
                updateStatus("P2P客户端初始化失败: ${e.message}")
            }
        }.start()
    }

    private fun setupCallbacks() {
        client?.let { c ->
            try {
                // 使用简单的函数引用
                val statusCallback = { status: String ->
                    mainHandler.post {
                        updateStatus(status)
                    }
                }
                c.javaClass.getMethod("SetStatusChangeCallback", Class.forName("java.lang.Object"))
                    .invoke(c, statusCallback)

                val errorCallback = { error: String ->
                    mainHandler.post {
                        appendLog("错误: $error")
                    }
                }
                c.javaClass.getMethod("SetErrorCallback", Class.forName("java.lang.Object"))
                    .invoke(c, errorCallback)

                val agentListCallback = { agents: Array<String> ->
                    mainHandler.post {
                        appendLog("Agent列表:\n${agents.joinToString("\n")}")
                    }
                }
                c.javaClass.getMethod("SetAgentListCallback", Class.forName("java.lang.Object"))
                    .invoke(c, agentListCallback)

                val speedTestCallback = { result: String ->
                    mainHandler.post {
                        appendLog("测速结果:\n$result")
                    }
                }
                c.javaClass.getMethod("SetSpeedTestCallback", Class.forName("java.lang.Object"))
                    .invoke(c, speedTestCallback)
            } catch (e: Exception) {
                e.printStackTrace()
                appendLog("设置回调失败: ${e.message}")
            }
        }
    }

    private fun startClient() {
        Thread {
            try {
                appendLog("正在启动客户端...")
                // 如果客户端为null，重新初始化
                if (client == null) {
                    client = MobileClient(
                        "/ip4/0.0.0.0/udp/0/quic-v1",
                        "/ip4/0.0.0.0/udp/0/kcp"
                    )
                    setupCallbacks()
                }

                client?.let { c ->
                    try {
                        c.start(58081, 58080)
                        mainHandler.post {
                            isClientRunning.value = true
                            updateStatus("客户端已启动")
                            appendLog("客户端启动成功，正在等待连接...")
                            appendLog("HTTP代理监听端口: 58080")
                            // 启动后自动执行bootstrap
                            bootstrap()
                            // 等待一段时间后再设置代理，确保Agent连接已建立
                            Thread.sleep(5000)
                            setProxy()
                            // 执行代理测试
                            testProxyConnection()
                        }
                    } catch (e: Exception) {
                        appendLog("调用Start方法失败: ${e.message}")
                        e.printStackTrace()
                    }
                }
            } catch (e: Exception) {
                mainHandler.post {
                    appendLog("启动失败: ${e.message}")
                    e.printStackTrace()
                }
            }
        }.start()
    }

    private fun stopClient() {
        Thread {
            try {
                appendLog("正在停止客户端...")
                // 在停止客户端前先测试一次
                testGoogleAccess()
                
                client?.stop()
                client = null  // 清除客户端实例
                mainHandler.post {
                    isClientRunning.value = false
                    updateStatus("客户端已停止")
                    appendLog("客户端已完全停止")
                    // 在停止客户端后再测试一次
                    testGoogleAccess()
                }
            } catch (e: Exception) {
                mainHandler.post {
                    appendLog("停止失败: ${e.message}")
                }
            }
        }.start()
    }

    private fun bootstrap() {
        Thread {
            try {
                appendLog("正在获取Agent列表...")
                client?.let { c ->
                    try {
                        // 直接调用Bootstrap方法，不使用反射
                        c.bootstrap()
                        appendLog("已发送获取Agent列表请求")

                        // 等待一段时间后获取Agent列表
                        Thread.sleep(2000)

                        // 获取并解析Agent列表
                        val agentsJson = c.getConnectedAgents()
                        appendLog("获取到的原始Agent列表JSON: $agentsJson")

                        if (agentsJson.isNullOrEmpty()) {
                            appendLog("Agent列表为空")
                            return@let
                        }

                        try {
                            val jsonArray = JSONArray(agentsJson)
                            if (jsonArray.length() == 0) {
                                appendLog("解析后的Agent列表为空")
                            } else {
                                val agents = mutableListOf<String>()
                                for (i in 0 until jsonArray.length()) {
                                    agents.add(jsonArray.getString(i))
                                }
                                appendLog("成功获取到 ${agents.size} 个Agent:")
                                agents.forEachIndexed { index, agent ->
                                    appendLog("Agent #${index + 1}: $agent")
                                }
                            }
                        } catch (e: Exception) {
                            appendLog("解析Agent列表JSON失败: ${e.message}")
                            e.printStackTrace()
                        }
                    } catch (e: Exception) {
                        appendLog("调用Bootstrap方法失败: ${e.message}")
                        e.printStackTrace()
                    }
                }
            } catch (e: Exception) {
                mainHandler.post {
                    appendLog("获取Agent列表失败: ${e.message}")
                    e.printStackTrace()
                }
            }
        }.start()
    }

    private fun runSpeedTest() {
        Thread {
            try {
                val agentsJson = client?.getConnectedAgents()
                appendLog("Raw agents JSON: $agentsJson")
                val agents = mutableListOf<String>()
                if (!agentsJson.isNullOrEmpty()) {
                    val jsonArray = JSONArray(agentsJson)
                    for (i in 0 until jsonArray.length()) {
                        agents.add(jsonArray.getString(i))
                    }
                }
                if (agents.isNotEmpty()) {
                    appendLog("开始与Agent ${agents[0]} 进行测速...")
                    client?.runSpeedTest(agents[0], 1024 * 1024, 10)
                    mainHandler.post {
                        currentStatus.value = "开始测速..."
                    }
                } else {
                    mainHandler.post {
                        appendLog("没有可用的Agent进行测速")
                    }
                }
            } catch (e: Exception) {
                mainHandler.post {
                    appendLog("测速失败: ${e.message}")
                    e.printStackTrace()
                }
            }
        }.start()
    }

    private fun stopSpeedTest() {
        Thread {
            try {
                client?.stopSpeedTest()
                mainHandler.post {
                    currentStatus.value = "测速已停止"
                    appendLog("停止测速成功")
                }
            } catch (e: Exception) {
                mainHandler.post {
                    appendLog("停止测速失败: ${e.message}")
                    e.printStackTrace()
                }
            }
        }.start()
    }

    private fun testHello() {
        Thread {
            try {
                val result = client?.testHello()
                mainHandler.post {
                    appendLog("TestHello结果: $result")
                }
            } catch (e: Exception) {
                mainHandler.post {
                    appendLog("TestHello失败: ${e.message}")
                    e.printStackTrace()
                }
            }
        }.start()
    }

    private fun updateStatus(status: String) {
        currentStatus.value = status
        appendLog(status)
    }

    private fun appendLog(message: String) {
        val timestamp = java.text.SimpleDateFormat("HH:mm:ss", Locale.getDefault()).format(Date())
        logMessages.add("[$timestamp] $message")
    }

    override fun onDestroy() {
        super.onDestroy()
        webView?.destroy()
        Thread {
            try {
                client?.stop()
            } catch (e: Exception) {
                e.printStackTrace()
            }
        }.start()
    }

    private fun initWebView(webView: WebView) {
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            setSupportZoom(true)
            builtInZoomControls = true
            displayZoomControls = false
            cacheMode = WebSettings.LOAD_DEFAULT
            setRenderPriority(WebSettings.RenderPriority.HIGH)
            setLayoutAlgorithm(WebSettings.LayoutAlgorithm.NARROW_COLUMNS)
            setEnableSmoothTransition(true)
            mediaPlaybackRequiresUserGesture = false
            mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
            setSavePassword(true)
            setSaveFormData(true)
            setJavaScriptCanOpenWindowsAutomatically(true)
            
            // 设置更长的超时时间
            setDefaultTextEncodingName("UTF-8")
//            setAppCacheEnabled(true)
            setDatabaseEnabled(true)
            setGeolocationEnabled(true)
            setNeedInitialFocus(true)
            setSupportMultipleWindows(true)
        }

        webView.setLayerType(View.LAYER_TYPE_HARDWARE, null)
        webView.setScrollBarStyle(WebView.SCROLLBARS_OUTSIDE_OVERLAY)
        webView.setScrollbarFadingEnabled(true)
        webView.setLongClickable(true)

        // 设置WebViewClient
        webView.webViewClient = object : WebViewClient() {
            override fun onPageStarted(view: WebView?, url: String?, favicon: android.graphics.Bitmap?) {
                super.onPageStarted(view, url, favicon)
                appendLog("开始加载页面: $url")
                appendLog("当前代理设置:")
                appendLog("HTTP代理: ${System.getProperty("http.proxyHost")}:${System.getProperty("http.proxyPort")}")
                appendLog("HTTPS代理: ${System.getProperty("https.proxyHost")}:${System.getProperty("https.proxyPort")}")
            }

            override fun onPageFinished(view: WebView?, url: String?) {
                super.onPageFinished(view, url)
                appendLog("页面加载完成: $url")
            }

            override fun onReceivedError(
                view: WebView?,
                request: android.webkit.WebResourceRequest?,
                error: android.webkit.WebResourceError?
            ) {
                super.onReceivedError(view, request, error)
                appendLog("页面加载错误: ${error?.description}")
                appendLog("错误URL: ${request?.url}")
                appendLog("错误类型: ${error?.errorCode}")
                appendLog("请求方法: ${request?.method}")
                appendLog("请求头: ${request?.requestHeaders}")

                if (error?.errorCode == android.webkit.WebViewClient.ERROR_CONNECT ||
                    error?.errorCode == android.webkit.WebViewClient.ERROR_TIMEOUT) {
                    appendLog("检测到连接超时，尝试重新设置代理...")
                    setProxy()
                }
            }

            override fun shouldOverrideUrlLoading(view: WebView?, url: String?): Boolean {
                appendLog("正在加载URL: $url")
                return false
            }

            override fun onReceivedSslError(
                view: WebView?,
                handler: android.webkit.SslErrorHandler?,
                error: android.net.http.SslError?
            ) {
                appendLog("SSL错误: ${error?.primaryError}")
                appendLog("SSL错误详情: ${error?.toString()}")
                handler?.proceed()
            }
        }

        // 设置WebChromeClient
        webView.webChromeClient = object : android.webkit.WebChromeClient() {
            override fun onConsoleMessage(consoleMessage: android.webkit.ConsoleMessage?): Boolean {
                appendLog("控制台消息: ${consoleMessage?.message()}")
                appendLog("控制台消息来源: ${consoleMessage?.sourceId()}")
                appendLog("控制台消息行号: ${consoleMessage?.lineNumber()}")
                return super.onConsoleMessage(consoleMessage)
            }

            override fun onProgressChanged(view: WebView?, newProgress: Int) {
                super.onProgressChanged(view, newProgress)
                appendLog("页面加载进度: $newProgress%")
                if (newProgress == 10) {
                    appendLog("警告: 页面加载停滞在10%")
                    appendLog("当前URL: ${view?.url}")
                    appendLog("当前标题: ${view?.title}")
                }
            }
        }
    }

    @Composable
    fun MainScreen(
        status: String,
        logs: List<String>,
        isRunning: Boolean,
        onStartClick: () -> Unit,
        onStopClick: () -> Unit,
        onBootstrapClick: () -> Unit,
        onSpeedTestClick: () -> Unit,
        onStopSpeedTestClick: () -> Unit,
        onTestHelloClick: () -> Unit
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(MaterialTheme.colorScheme.background)
                .padding(16.dp)
        ) {
            // 顶部状态卡片
            ElevatedCard(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 16.dp),
                colors = CardDefaults.elevatedCardColors(
                    containerColor = MaterialTheme.colorScheme.surfaceVariant
                )
            ) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(16.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    // 状态指示器
                    Box(
                        modifier = Modifier
                            .size(12.dp)
                            .background(
                                color = if (isRunning)
                                    MaterialTheme.colorScheme.primary
                                else
                                    MaterialTheme.colorScheme.error,
                                shape = CircleShape
                            )
                    )
                    Spacer(modifier = Modifier.width(12.dp))
                    Text(
                        text = status,
                        style = MaterialTheme.typography.titleMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }

            // 按钮区域
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 16.dp)
            ) {
                // 主要操作按钮
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(bottom = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    Button(
                        onClick = onStartClick,
                        modifier = Modifier.weight(1f),
                        enabled = !isRunning,
                        colors = ButtonDefaults.buttonColors(
                            containerColor = MaterialTheme.colorScheme.primary
                        )
                    ) {
                        Icon(
                            imageVector = Icons.Default.PlayArrow,
                            contentDescription = "启动",
                            modifier = Modifier.size(20.dp)
                        )
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("启动客户端")
                    }
                    Button(
                        onClick = onStopClick,
                        modifier = Modifier.weight(1f),
                        enabled = isRunning,
                        colors = ButtonDefaults.buttonColors(
                            containerColor = MaterialTheme.colorScheme.error
                        )
                    ) {
                        Icon(
                            imageVector = Icons.Default.Close,
                            contentDescription = "停止",
                            modifier = Modifier.size(20.dp)
                        )
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("停止客户端")
                    }
                }

                // 功能按钮组
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(bottom = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    OutlinedButton(
                        onClick = onBootstrapClick,
                        modifier = Modifier.weight(1f),
                        enabled = isRunning,
                        border = BorderStroke(1.dp, MaterialTheme.colorScheme.primary)
                    ) {
                        Icon(
                            imageVector = Icons.Default.Refresh,
                            contentDescription = "刷新",
                            modifier = Modifier.size(20.dp)
                        )
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("获取Agent列表")
                    }
                    OutlinedButton(
                        onClick = onSpeedTestClick,
                        modifier = Modifier.weight(1f),
                        enabled = isRunning,
                        border = BorderStroke(1.dp, MaterialTheme.colorScheme.primary)
                    ) {
                        Icon(
                            imageVector = Icons.Default.Refresh,
                            contentDescription = "测速",
                            modifier = Modifier.size(20.dp)
                        )
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("测速")
                    }
                }

                // 测速控制按钮
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(bottom = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    OutlinedButton(
                        onClick = onStopSpeedTestClick,
                        modifier = Modifier.weight(1f),
                        enabled = isRunning,
                        border = BorderStroke(1.dp, MaterialTheme.colorScheme.error)
                    ) {
                        Icon(
                            imageVector = Icons.Default.Close,
                            contentDescription = "停止测速",
                            modifier = Modifier.size(20.dp)
                        )
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("停止测速")
                    }
                    OutlinedButton(
                        onClick = onTestHelloClick,
                        modifier = Modifier.weight(1f),
                        enabled = isRunning,
                        border = BorderStroke(1.dp, MaterialTheme.colorScheme.primary)
                    ) {
                        Icon(
                            imageVector = Icons.Default.Check,
                            contentDescription = "测试",
                            modifier = Modifier.size(20.dp)
                        )
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("TestHello")
                    }
                }
            }

            // 日志区域
            ElevatedCard(
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(0.3f),
                colors = CardDefaults.elevatedCardColors(
                    containerColor = MaterialTheme.colorScheme.surfaceVariant
                )
            ) {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .verticalScroll(rememberScrollState())
                        .padding(16.dp)
                ) {
                    Text(
                        text = "运行日志",
                        style = MaterialTheme.typography.titleMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(bottom = 8.dp)
                    )
                    logs.forEach { log ->
                        Text(
                            text = log,
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(vertical = 4.dp)
                        )
                    }
                }
            }

            // 浏览器区域
            ElevatedCard(
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(0.7f)
                    .padding(top = 16.dp),
                colors = CardDefaults.elevatedCardColors(
                    containerColor = MaterialTheme.colorScheme.surfaceVariant
                )
            ) {
                Column(modifier = Modifier.fillMaxSize()) {
                    // 导航按钮区域
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(16.dp),
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        listOf(
                            "Bing" to "https://www.bing.com"
                        ).forEach { (name, url) ->
                            Button(
                                onClick = {
                                    appendLog("点击导航按钮: $name")
                                    webView?.loadUrl(url)
                                },
                                modifier = Modifier.weight(1f)
                            ) {
                                Text(name)
                            }
                        }
                    }

                    // WebView区域
                    Box(modifier = Modifier.fillMaxSize()) {
                        AndroidView(
                            modifier = Modifier.fillMaxSize(),
                            factory = { context ->
                                WebView(context).apply {
                                    layoutParams = ViewGroup.LayoutParams(
                                        ViewGroup.LayoutParams.MATCH_PARENT,
                                        ViewGroup.LayoutParams.MATCH_PARENT
                                    )
                                    initWebView(this)
                                    webView = this
                                    loadUrl("https://www.google.com")
                                }
                            }
                        )
                    }
                }
            }
        }
    }

}

@Composable
fun Greeting(name: String, modifier: Modifier = Modifier) {
    Text(
        text = "Hello $name!",
        modifier = modifier
    )
}

@Preview(showBackground = true)
@Composable
fun GreetingPreview() {
    DemoTheme {
        Greeting("Android")
    }
}