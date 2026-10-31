#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
if [ -n "$(gofmt -l $(find . -name '*.go' -type f))" ]; then
  echo 'Go files need formatting (gofmt).' >&2
  exit 1
fi
sh -n install.sh
python3 scripts/test_install.py
python3 scripts/test_release.py
go test ./...
go vet ./...
python3 scripts/generate_protocol.py --schema protocol/v1/envelope.schema.json --check
go build -o /tmp/viorka-scaffold-agent ./cmd/viorka-agent
python3 -m json.tool examples/agent.example.json >/dev/null
echo 'Viorka Agent M0 check OK'
