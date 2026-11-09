#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
step=0
check() {
  step=$((step + 1))
  label=$1
  shift
  started=$(date +%s)
  printf '[validate] [%s/13] %s  检查中…\n' "$step" "$label"
  if "$@"; then
    printf '[validate] [%s/13] %s  通过  %s 秒\n' "$step" "$label" "$(($(date +%s) - started))"
  else
    result=$?
    printf '[validate] [%s/13] %s  失败  %s 秒\n' "$step" "$label" "$(($(date +%s) - started))"
    exit "$result"
  fi
}
check_format() {
  if [ -n "$(gofmt -l $(find . -name '*.go' -type f))" ]; then
    echo 'Go files need formatting (gofmt).' >&2
    return 1
  fi
}
check_json() {
  python3 -m json.tool examples/agent.example.json >/dev/null
}
check 'Go 格式' check_format
check '安装脚本语法' sh -n install.sh
check '安装脚本测试' python3 scripts/test_install.py
check '服务管理测试' python3 scripts/test_services.py
check '卸载脚本测试' python3 scripts/test_uninstall.py
check '发布流程测试' python3 scripts/test_release.py
check 'Go 全量测试' go test ./...
# Host tests omit *_linux_test.go on macOS. Compile every test package for
# supported Linux targets as well; this does not execute Linux tests locally.
(
  test_build_dir=$(mktemp -d "${TMPDIR:-/tmp}/viorka-agent-tests.XXXXXX")
  trap 'rm -rf "$test_build_dir"' 0
  for target_arch in amd64 arm64; do
    mkdir "$test_build_dir/$target_arch"
    check "Linux $target_arch 测试编译" env CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go test -c -o "$test_build_dir/$target_arch/" ./...
  done
)
step=9
check 'Go 静态检查' go vet ./...
check '协议生成一致性' python3 scripts/generate_protocol.py --schema protocol/v1/envelope.schema.json --check
check '本机 Agent 构建' go build -o /tmp/viorka-scaffold-agent ./cmd/viorka-agent
check '示例配置 JSON' check_json
echo 'Viorka Agent M0 check OK'
