# Lyrelog CLI. `make help` lists the targets.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X go.lyrelog.com/cli/internal/cli.Version=$(VERSION) -X go.lyrelog.com/cli/internal/cli.Commit=$(COMMIT) -X go.lyrelog.com/cli/internal/cli.Date=$(DATE)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: help build install test vet check golden dist clean skills

help: ## This list
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## The binary, at bin/lyrelog
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/lyrelog ./cmd/lyrelog

install: ## go install it (into $$GOBIN or ~/go/bin)
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/lyrelog

vet: ## go vet
	go vet ./...

test: vet ## vet, then the tests with the race detector
	go test -race ./...

check: test ## test, staticcheck, and a build for every platform
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
	@$(MAKE) --no-print-directory dist

golden: ## Rewrite the golden files after a deliberate change to the output (review the diff)
	go test ./internal/cli -update

dist: ## Binaries for macOS, Linux and Windows, in dist/
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "  dist/lyrelog-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/lyrelog-$$os-$$arch$$ext ./cmd/lyrelog || exit 1; \
	done

skills: ## Refresh the skills inside the binary from ../lyrelog-skills (the offline copy; install gets the newest from GitHub)
	rm -rf internal/skills/files && mkdir -p internal/skills/files
	cp -R ../lyrelog-skills/skills/. internal/skills/files/

clean: ## Remove bin/ and dist/
	rm -rf bin dist
