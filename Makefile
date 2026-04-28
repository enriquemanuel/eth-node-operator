BINARY_AGENT   := ethagent
BINARY_CTL     := ethctl
MODULE         := github.com/enriquemanuel/eth-node-operator
VERSION        := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS        := -ldflags "-s -w -X main.version=$(VERSION)"
DIST           := dist

.PHONY: all build build-agent build-ctl test test-race lint fmt vet tidy clean install \
        install-agent install-ctl release-linux release-darwin run-agent

all: lint test build

## ──────────────────────────────── Build ────────────────────────────────

build: build-agent build-ctl

build-agent:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(DIST)/$(BINARY_AGENT) ./cmd/agent
	@echo "Built $(DIST)/$(BINARY_AGENT)"

build-ctl:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(DIST)/$(BINARY_CTL) ./cmd/ethctl
	@echo "Built $(DIST)/$(BINARY_CTL)"

release-linux:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(DIST)/$(BINARY_AGENT)-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(DIST)/$(BINARY_CTL)-linux-amd64 ./cmd/ethctl
	@echo "Built linux/amd64 binaries in $(DIST)/"

release-darwin:
	@mkdir -p $(DIST)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o $(DIST)/$(BINARY_CTL)-darwin-arm64 ./cmd/ethctl
	@echo "Built darwin/arm64 ethctl in $(DIST)/"

## ──────────────────────────────── Test ─────────────────────────────────

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

test-cover:
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-verbose:
	go test -v ./... -count=1

## ──────────────────────────────── Quality ───────────────────────────────

lint:
	@which golangci-lint > /dev/null || (echo "golangci-lint not found, run: brew install golangci-lint" && exit 1)
	golangci-lint run ./...

fmt:
	gofmt -w -s .
	goimports -w . 2>/dev/null || true

vet:
	go vet ./...

tidy:
	go mod tidy

## ──────────────────────────────── Install ───────────────────────────────

install: install-agent install-ctl

install-agent: build-agent
	cp $(DIST)/$(BINARY_AGENT) /usr/local/bin/$(BINARY_AGENT)
	@echo "Installed /usr/local/bin/$(BINARY_AGENT)"

install-ctl: build-ctl
	cp $(DIST)/$(BINARY_CTL) /usr/local/bin/$(BINARY_CTL)
	@echo "Installed /usr/local/bin/$(BINARY_CTL)"

install-systemd: install-agent
	cp deploy/systemd/ethagent.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable ethagent
	systemctl start ethagent
	@echo "ethagent installed as systemd service"

## ──────────────────────────────── Run ───────────────────────────────────

run-agent:
	go run ./cmd/agent \
		--spec=inventory/nodes/ovh-bare-01.yaml \
		--listen=:9000 \
		--log-level=debug

## ──────────────────────────────── Clean ────────────────────────────────

clean:
	rm -rf $(DIST)/ coverage.out coverage.html

## ──────────────────────────────── Help ─────────────────────────────────

help:
	@echo "eth-node-operator Makefile targets:"
	@echo ""
	@echo "  make build          Build agent + ethctl binaries"
	@echo "  make test           Run all tests"
	@echo "  make test-race      Run tests with race detector"
	@echo "  make test-cover     Run tests with coverage HTML report"
	@echo "  make lint           Run golangci-lint"
	@echo "  make fmt            Format code"
	@echo "  make tidy           Tidy go.mod"
	@echo "  make install        Install binaries to /usr/local/bin"
	@echo "  make install-systemd Install + enable as systemd service"
	@echo "  make release-linux  Cross-compile for linux/amd64"
	@echo "  make release-darwin Cross-compile ethctl for darwin/arm64"
	@echo "  make clean          Remove build artifacts"
