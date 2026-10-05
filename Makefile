ARCH ?= amd64
HOST ?= bot-server
REMOTE_DIR = /opt/bot

.PHONY: run test test-live lint build-linux deploy

run:
	set -a; . ./.env; set +a; go run ./cmd/bot

test:
	go test -race ./...

test-live:
	LLM_LIVE=1 go test ./internal/agent/...

# Linters run through go run: binaries built with an older Go cannot read this module.
lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	@out="$$(go run mvdan.cc/gofumpt@latest -l internal cmd)"; test -z "$$out" || { echo "$$out"; exit 1; }
	go run honnef.co/go/tools/cmd/staticcheck@latest -checks all ./...
	go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./internal/... ./cmd/...

build-linux:
	GOOS=linux GOARCH=$(ARCH) CGO_ENABLED=0 go build -trimpath -o bin/bot ./cmd/bot

# HOST is an ssh host alias, e.g. from ~/.ssh/config: make deploy HOST=my-vps
deploy: build-linux
	scp bin/bot $(HOST):/tmp/bot
	ssh $(HOST) 'sudo install -m 755 /tmp/bot $(REMOTE_DIR)/bot && sudo systemctl restart bot'
