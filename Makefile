.PHONY: doctor test fmt-check build release
.DEFAULT_GOAL := doctor

release:
	python3 scripts/release.py

doctor:
	go run ./cmd/viorka-agent doctor

test:
	go test ./...

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -type f))" || (echo 'Run gofmt -w on Go files' && exit 1)

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/viorka-agent ./cmd/viorka-agent
