#!/bin/sh
# 构建 macOS / Linux 版本
#   ./build.sh            生成 dist/agyproxy-<os>-<arch>
#   ./build.sh --install  构建后安装到 ~/.local/bin 并创建桌面图标
set -e
cd "$(dirname "$0")"
mkdir -p dist

go vet ./...

os=$(go env GOOS)
arch=$(go env GOARCH)
out="dist/agyproxy-$os-$arch"
go build -trimpath -ldflags "-s -w" -o "$out" .

echo "构建完成: $out"
if [ "$1" = "--install" ]; then
	"./$out" -install
else
	echo "安装为全局命令: ./$out -install"
fi
