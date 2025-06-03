@echo off
set ANDROID_HOME=D:\dev\Android\SDK3
set ANDROID_NDK_HOME=D:\dev\Android\SDK3\ndk\23.1.7779620
set GO111MODULE=on
set CGO_ENABLED=1
set GOARCH=amd64
set GOOS=windows

echo Checking NDK installation...
dir %ANDROID_NDK_HOME%\toolchains\llvm\prebuilt\windows-x86_64\bin\armv7a-linux-androideabi21-clang*
dir %ANDROID_NDK_HOME%\toolchains\llvm\prebuilt\windows-x86_64\bin\aarch64-linux-android21-clang*
dir %ANDROID_NDK_HOME%\toolchains\llvm\prebuilt\windows-x86_64\bin\x86_64-linux-android21-clang*

echo Installing gomobile...
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest

echo Initializing gomobile...
gomobile init

echo Building...
gomobile bind -ldflags "-checklinkname=0" -target=android -androidapi 21 -o ../../Demo/app/libs/androidp2p.aar -v ./