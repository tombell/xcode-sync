VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS = -X main.Version=$(VERSION) -X main.Commit=$(COMMIT)

.PHONY: build test prod clean

build:
	mkdir -p dist
	go build -ldflags "$(LDFLAGS)" -o dist/xcode-sync ./cmd/xcode-sync

test:
	go test ./...

prod:
	mkdir -p dist
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/xcode-sync-darwin-amd64 ./cmd/xcode-sync
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/xcode-sync-darwin-arm64 ./cmd/xcode-sync

clean:
	rm -rf dist
