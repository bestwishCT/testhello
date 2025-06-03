@echo off
setlocal enabledelayedexpansion

:: 设置环境变量
set GO111MODULE=on
set CGO_ENABLED=1
set GOOS=ios
set GOARCH=arm64

:: 清理旧的构建文件
if exist *.framework (
    echo Cleaning old framework files...
    rmdir /s /q *.framework
)

:: 设置构建参数
set BUILD_TAGS=ios
set BUILD_LDFLAGS=-s -w
set BUILD_DEBUG=false

:: 使用 gomobile 构建 iOS 框架
echo Building iOS framework...
cd ..\..
gomobile bind -target=ios ^
    -o ios/ShileP2P.framework ^
    -prefix=ShileP2P ^
    -tags=%BUILD_TAGS% ^
    -ldflags="%BUILD_LDFLAGS%" ^
    -v=%BUILD_DEBUG% ^
    ./mobile

:: 检查构建结果
if %ERRORLEVEL% equ 0 (
    echo.
    echo iOS framework build successful!
    echo Output: ios/ShileP2P.framework
    
    :: 检查文件大小
    for %%F in (ios/ShileP2P.framework) do (
        set "size=%%~zF"
        set /a "size_kb=!size! / 1024"
        set /a "size_mb=!size_kb! / 1024"
        echo Framework size: !size_mb! MB (!size_kb! KB)
    )
) else (
    echo.
    echo Build failed, please check error messages
    exit /b 1
)

:: 清理临时文件
if exist *.a (
    del /f /q *.a
)

endlocal 