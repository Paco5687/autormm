# autormm — build & packaging
#
# Requires Go 1.26+. All binaries are static and CGO-free, so they cross-compile
# without a C toolchain.

VERSION ?= 0.1.0
LDFLAGS := -s -w -X github.com/Paco5687/autormm/agent.Version=$(VERSION)
GO      := go
DIST    := dist

.PHONY: all build test vet tidy clean dist run-server embed-agents

# Agent binaries the hub embeds and serves for one-command installs.
AGENT_TARGETS := linux/amd64 linux/arm64 windows/amd64

all: build

embed-agents: ## build agent binaries into server/agentbins for the hub to serve
	@mkdir -p server/agentbins
	@for t in $(AGENT_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
	  echo "  embed agent $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" \
	    -o server/agentbins/autormm-agent_$${os}_$${arch}$$ext ./cmd/autormm-agent; \
	done
	@echo "  embed agent-tray windows/amd64 (GUI)"
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS) -H=windowsgui" \
	  -o server/agentbins/autormm-agent-tray_windows_amd64.exe ./cmd/autormm-agent-tray

build: embed-agents ## build native binaries into dist/ (hub embeds agents)
	@mkdir -p $(DIST)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-server ./cmd/autormm-server
	$(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-agent  ./cmd/autormm-agent
	$(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-client ./cmd/autormm-client
	@echo "built -> $(DIST)/"

test: ## run the test suite
	$(GO) test ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

# Cross-compiled, release-ready binaries. Server runs on the hub (linux/amd64);
# agents & client ship for the common host platforms.
dist: embed-agents ## cross-compile release binaries for all targets
	@mkdir -p $(DIST)
	# hub server (linux/amd64)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-server-linux-amd64       ./cmd/autormm-server
	# agents
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-agent-linux-amd64        ./cmd/autormm-agent
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-agent-linux-arm64        ./cmd/autormm-agent
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-agent-windows-amd64.exe  ./cmd/autormm-agent
	# client
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-client-linux-amd64       ./cmd/autormm-client
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-client-windows-amd64.exe ./cmd/autormm-client
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/autormm-client-darwin-arm64      ./cmd/autormm-client
	@echo "release binaries -> $(DIST)/"
	@ls -1 $(DIST)

run-server: build ## run the server locally on :8765
	AUTORMM_ADMIN_TOKEN=$${AUTORMM_ADMIN_TOKEN:-dev-admin} \
	AUTORMM_ENROLL_TOKEN=$${AUTORMM_ENROLL_TOKEN:-dev-enroll} \
	$(DIST)/autormm-server -addr 127.0.0.1:8765

clean:
	rm -rf $(DIST)
