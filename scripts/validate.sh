#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
if [ -n "$(gofmt -l $(find . -name '*.go' -type f))" ]; then
  echo 'Go files need formatting (gofmt).' >&2
  exit 1
fi
go test ./...
go build -o /tmp/viorka-scaffold-agent ./cmd/viorka-agent
python3 -m json.tool examples/agent.example.json >/dev/null
echo 'Viorka Agent scaffold check OK'
