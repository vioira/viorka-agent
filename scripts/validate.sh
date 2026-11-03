#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
if [ -n "$(gofmt -l $(find . -name '*.go' -type f))" ]; then
  echo 'Go files need formatting (gofmt).' >&2
  exit 1
fi
sh -n install.sh
python3 scripts/test_install.py
python3 scripts/test_services.py
python3 scripts/test_uninstall.py
python3 scripts/test_release.py
go test ./...
# Host tests omit *_linux_test.go on macOS. Compile every test package for
# supported Linux targets as well; this does not execute Linux tests locally.
(
  test_build_dir=$(mktemp -d "${TMPDIR:-/tmp}/viorka-agent-tests.XXXXXX")
  trap 'rm -rf "$test_build_dir"' 0
  for target_arch in amd64 arm64; do
    mkdir "$test_build_dir/$target_arch"
    CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" go test -c -o "$test_build_dir/$target_arch/" ./...
  done
)
go vet ./...
python3 scripts/generate_protocol.py --schema protocol/v1/envelope.schema.json --check
go build -o /tmp/viorka-scaffold-agent ./cmd/viorka-agent
python3 -m json.tool examples/agent.example.json >/dev/null
echo 'Viorka Agent M0 check OK'
