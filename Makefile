.PHONY: run test validate build build-mcp build-reconciler build-darwin install-claude-desktop setup doctor uninstall up down logs

run:
	go run ./cmd/todod

test:
	go test -race ./...
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt required" && gofmt -l . && exit 1)

build:
	CGO_ENABLED=0 go build -trimpath -o bin/todod ./cmd/todod

validate: test
	bash -n scripts/todo
	bash -n scripts/board-context.sh
	bash -n scripts/board-notifier
	bash -n scripts/install-board-notifier
	bash -n scripts/install-board-reconciler
	bash -n scripts/install-claude-desktop-mcp
	bash -n scripts/install-codex-integration
	bash -n scripts/team-setup
	bash -n scripts/team-uninstall
	bash -n scripts/board-doctor
	bash -n plugin/scripts/session-start.sh
	jq -e . .claude-plugin/marketplace.json plugin/.claude-plugin/plugin.json plugin/hooks/hooks.json >/dev/null
	@if command -v claude >/dev/null 2>&1; then claude plugin validate .; else echo "claude not installed; skipped plugin validate"; fi
	node scripts/check-web.mjs
	deno fmt --check webui/board.html scripts/check-web.mjs
	docker compose config --quiet

build-mcp:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/todo-mcp ./cmd/todo-mcp

build-reconciler:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/board-reconciler ./cmd/board-reconciler

GO_IMAGE := golang:1.27.1-alpine3.24
DARWIN_ARCH := $(if $(filter x86_64,$(shell uname -m)),amd64,arm64)

build-darwin:
	mkdir -p bin
	@if command -v go >/dev/null 2>&1; then \
		$(MAKE) build-mcp build-reconciler; \
	else \
		docker run --rm -v "$(CURDIR)":/src -w /src -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=$(DARWIN_ARCH) -e GOFLAGS=-buildvcs=false $(GO_IMAGE) \
			sh -c 'go build -trimpath -o bin/todo-mcp ./cmd/todo-mcp && go build -trimpath -o bin/board-reconciler ./cmd/board-reconciler'; \
	fi

install-claude-desktop:
	./scripts/install-claude-desktop-mcp

setup:
	./scripts/team-setup

doctor:
	./scripts/board-doctor

uninstall:
	./scripts/team-uninstall

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f board
