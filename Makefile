VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BINARY  := orch
INSTALL := $(HOME)/go/bin/$(BINARY)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test lint clean install setup

## build: 編譯 binary 到當前目錄
build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/orch/

## test: 跑所有測試
test:
	go test ./...

## lint: 靜態檢查
lint:
	go vet ./...

## clean: 清除產物
clean:
	rm -f $(BINARY) coverage.out coverage.html

## install: 編譯 + 安裝到 ~/go/bin + 初始化設定
install: build
	@mkdir -p $(HOME)/go/bin
	cp $(BINARY) $(INSTALL)
	@echo "✅ installed: $(INSTALL) ($(VERSION))"
	@mkdir -p $(HOME)/.config/orch
	@if [ ! -f $(HOME)/.config/orch/config.yaml ]; then \
		cp config.yaml $(HOME)/.config/orch/config.yaml; \
		echo "✅ config copied to ~/.config/orch/config.yaml"; \
	else \
		echo "⏭️  config already exists, skipping"; \
	fi
	@echo ""
	@echo "Run 'make setup' for full environment (MLX + model + daemon)"

## setup: 完整安裝（MLX env + model + launchd daemon）
setup: install
	./setup.sh

## cover: 測試覆蓋率報告
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "✅ coverage report: coverage.html"

## help: 顯示可用目標
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
