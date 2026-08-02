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

# Nguồn của thư viện UI được vendor vào internal/webui/assets/vendor/puredashboard.
PD_REPO ?= https://github.com/madnh/puredashboard.git
PD_REF  ?= main
PD_DIR  := internal/webui/assets/vendor/puredashboard

# Công cụ phát triển: pin phiên bản ở đây để mọi máy và mọi phiên Claude Code dùng
# CÙNG một bản. gopls là language server của Go — nó cho biết một ký hiệu được dùng ở
# đâu theo đúng ngữ nghĩa, thứ mà grep không làm được (grep không phân biệt nổi
# `Result.Sections` với `Pad.Sections`, đúng cái bẫy target `layers` đã vấp phải).
GOPLS_VERSION ?= latest

.PHONY: help build-dev build-release install run ui test fmt fmt-check vet layers tidy check clean vendor-ui tools

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

ui: ## Chạy Web UI (ui) — thêm ARGS="..." để truyền cờ, vd: make ui ARGS="--port 7000"
	go run -ldflags "$(LDFLAGS)" $(PKG) ui $(ARGS)

vendor-ui: ## Cập nhật thư viện UI đã vendor từ puredashboard (PD_REF=<branch|tag|commit>)
	@tmp=$$(mktemp -d) && \
	git clone --quiet --depth 1 --branch $(PD_REF) $(PD_REPO) $$tmp && \
	rm -rf $(PD_DIR) && mkdir -p $(PD_DIR)/theme && \
	cp $$tmp/src/*.js $$tmp/src/*.css $(PD_DIR)/ && \
	cp $$tmp/src/theme/*.css $(PD_DIR)/theme/ && \
	cp $$tmp/LICENSE $(PD_DIR)/LICENSE && \
	printf 'source: %s\ncommit: %s\ndate:   %s\n' \
		"$(PD_REPO)" "$$(git -C $$tmp rev-parse HEAD)" "$$(git -C $$tmp log -1 --date=short --format=%ad)" > $(PD_DIR)/VERSION && \
	rm -rf $$tmp && \
	echo "vendored $(PD_REF) → $(PD_DIR) (nhớ ghi lại phần ghi chú trong VERSION rồi commit)"

test: ## Chạy toàn bộ test
	go test ./...

fmt: ## Định dạng lại code (gofmt -w)
	gofmt -w .

fmt-check: ## Kiểm tra định dạng — fail nếu có file chưa gofmt
	@files=$$(gofmt -l .); if [ -n "$$files" ]; then echo "chưa gofmt:"; echo "$$files"; exit 1; fi

vet: ## Phân tích tĩnh bằng go vet
	go vet ./...

# Ngoài internal/pad, không nơi nào được tự duyệt danh sách section.
#
# Trước khi có internal/pad, 14 chỗ trong mcpsrv/webui/cmd tự duyệt pad.Sections, và
# "chọn section nào" tồn tại dưới 3 từ vựng khác nhau cho cùng một khái niệm. Mọi phép
# suy diễn (turn, task, participants) và phép chọn giờ nằm ở internal/pad; các surface
# chỉ dịch request thành pad.Selector. Đây là luật giữ cho điều đó không mục lại — và
# nó tự kiểm tra được, khác với một dòng văn xuôi trong CLAUDE.md.
# Bỏ qua *_test.go (test được phép khẳng định trực tiếp trên cấu trúc đã parse) và
# `Select(...).Sections` (đó là kết quả của Selector, tức là đang DÙNG đúng cơ chế).
layers: ## Kiểm tra ranh giới: chỉ internal/pad được duyệt Sections
	@bad=$$(grep -rn "range .*\.Sections\|\.Sections\[" --include=*.go \
		internal cmd 2>/dev/null \
		| grep -v "^internal/pad/" | grep -v "_test\.go:" | grep -v ")\.Sections" || true); \
	if [ -n "$$bad" ]; then \
		echo "chỉ internal/pad được duyệt danh sách section — hãy dùng pad.Selector / các hàm suy diễn:"; \
		echo "$$bad"; exit 1; fi

# Kiểm tra cả PATH lẫn $GOPATH/bin: `go install` đặt binary vào $GOPATH/bin, thư mục
# này thường CHƯA nằm trong PATH — nếu chỉ hỏi `command -v` thì lần chạy nào cũng cài
# lại, và target mất tính idempotent đúng lúc hook cần nó nhất.
tools: ## Cài công cụ phát triển (gopls) — idempotent, chạy lại bao nhiêu lần cũng được
	@gobin=$$(go env GOBIN); [ -n "$$gobin" ] || gobin=$$(go env GOPATH)/bin; \
	if command -v gopls >/dev/null 2>&1; then \
		echo "gopls đã có: $$(command -v gopls) ($$(gopls version 2>/dev/null | head -1))"; \
	elif [ -x "$$gobin/gopls" ]; then \
		echo "gopls đã có: $$gobin/gopls ($$($$gobin/gopls version 2>/dev/null | head -1))"; \
		echo "nhưng $$gobin KHÔNG có trong PATH — plugin gopls-lsp sẽ không tìm thấy nó"; \
	else \
		echo "cài gopls@$(GOPLS_VERSION)…"; \
		go install golang.org/x/tools/gopls@$(GOPLS_VERSION); \
		echo "xong → $$gobin/gopls — nhớ để $$gobin trong PATH"; \
	fi

tidy: ## Dọn go.mod/go.sum
	go mod tidy

check: fmt-check vet layers test ## Cổng kiểm tra trước khi commit (fmt-check + vet + layers + test)

clean: ## Xoá artifact build
	rm -rf $(BIN_DIR)
	go clean
