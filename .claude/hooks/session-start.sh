#!/bin/bash
# SessionStart — chuẩn bị môi trường cho một phiên Claude Code trên web.
#
# Container của phiên web là ephemeral: mỗi phiên clone lại repo từ đầu, nên module
# cache trống và chưa có công cụ nào. Hook này lấp đúng khoảng đó, để phiên bắt đầu là
# `make check` chạy được ngay và plugin gopls-lsp tìm thấy language server.
#
# Trên MÁY của bạn hook này không chạy (xem CLAUDE_CODE_REMOTE bên dưới) — ở đó bạn tự
# chủ động, và `make tools` là lệnh duy nhất cần biết.
set -euo pipefail

# Máy cá nhân đã có sẵn môi trường; đừng tự ý cài gì vào đó.
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-.}"

# go install đặt binary vào đây, và thư mục này KHÔNG mặc định nằm trong PATH của
# container. Ghi vào CLAUDE_ENV_FILE để mọi lệnh trong phiên — kể cả plugin gopls-lsp
# đi tìm language server — nhìn thấy nó.
GOBIN_DIR="$(go env GOBIN)"
[ -n "$GOBIN_DIR" ] || GOBIN_DIR="$(go env GOPATH)/bin"
export PATH="$PATH:$GOBIN_DIR"
if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  echo "export PATH=\"\$PATH:$GOBIN_DIR\"" >> "$CLAUDE_ENV_FILE"
fi

# Làm ấm module cache. Trạng thái container được cache lại sau khi hook xong, nên công
# này chỉ trả một lần cho nhiều phiên.
echo "→ go mod download"
go mod download

# gopls: language server cho Go. Plugin gopls-lsp chỉ BỌC nó, không bundle — không có
# binary này thì plugin im lặng không hoạt động.
echo "→ make tools"
make tools

# Build một lần để cache luôn cả build cache: `make check` ngay sau đó sẽ nhanh hơn
# nhiều, và nếu repo đang hỏng thì phiên biết ngay từ đầu thay vì lúc đang sửa dở.
echo "→ go build ./..."
go build ./...

echo "sẵn sàng: make check | make tools | make run"
