# 构建 Windows 版本
#   .\build.ps1          生成 dist\agyproxy.exe（控制台版）与 dist\agyproxyw.exe（无窗口版）
#   .\build.ps1 -Install 构建后安装到用户目录、写入 PATH 并创建桌面图标

param([switch]$Install)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
New-Item -ItemType Directory -Force -Path dist | Out-Null

go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet 失败' }

go build -trimpath -ldflags "-s -w" -o dist\agyproxy.exe .
if ($LASTEXITCODE -ne 0) { throw '构建控制台版失败' }

go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist\agyproxyw.exe .
if ($LASTEXITCODE -ne 0) { throw '构建无窗口版失败' }

Write-Host '构建完成：'
Write-Host '  dist\agyproxy.exe   控制台版，命令行/调试用'
Write-Host '  dist\agyproxyw.exe  无窗口版，桌面图标指向它'

if ($Install) {
    & .\dist\agyproxy.exe -install
}
