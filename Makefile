# Makefile — các lệnh hay dùng cho scratchpad.
# Mỗi target có chú thích "## …" ở cuối dòng; `make help` (mặc định) in ra danh sách đó.

BIN_DIR := bin
BINARY  := $(BIN_DIR)/scratchpad
PKG     := ./cmd/scratchpad

# Version/commit/date nhúng vào binary qua -ldflags (xem internal/buildinfo).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/madnh/scratchpad/internal/buildinfo.Version=$(VERSION) \
           -X github.com/madnh/scratchpad/internal/buildinfo.Commit=$(COMMIT) \
           -X github.com/madnh/scratchpad/internal/buildinfo.Date=$(DATE)

.DEFAULT_GOAL := help

.PHONY: help build-dev build-release install run test fmt fmt-check vet tidy check clean

help: ## In danh sách lệnh (mặc định)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build-dev: ## Build bản DEV (giữ debug symbol, KHÔNG trimpath) → ./bin/scratchpad — để phát triển/debug, KHÔNG mang đi phân phối
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

build-release: ## Build bản PHÁT HÀNH (strip -s -w + -trimpath) → ./bin/scratchpad — bản mang đi phân phối, khớp GoReleaser
	go build -trimpath -ldflags "$(LDFLAGS) -s -w" -o $(BINARY) $(PKG)

install: ## Cài binary vào $GOBIN (hoặc $GOPATH/bin)
	go install -ldflags "$(LDFLAGS)" $(PKG)

run: ## Chạy server (serve) — thêm ARGS="..." để truyền cờ, vd: make run ARGS="--stdio"
	go run -ldflags "$(LDFLAGS)" $(PKG) serve $(ARGS)

test: ## Chạy toàn bộ test
	go test ./...

fmt: ## Định dạng lại code (gofmt -w)
	gofmt -w .

fmt-check: ## Kiểm tra định dạng — fail nếu có file chưa gofmt
	@files=$$(gofmt -l .); if [ -n "$$files" ]; then echo "chưa gofmt:"; echo "$$files"; exit 1; fi

vet: ## Phân tích tĩnh bằng go vet
	go vet ./...

tidy: ## Dọn go.mod/go.sum
	go mod tidy

check: fmt-check vet test ## Cổng kiểm tra trước khi commit (fmt-check + vet + test)

clean: ## Xoá artifact build
	rm -rf $(BIN_DIR)
	go clean
