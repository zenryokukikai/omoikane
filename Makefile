.PHONY: build test test-race test-cover lint run clean fmt vet tidy

GO       ?= go
GOFLAGS  ?= -tags sqlite_fts5
BIN_DIR  := bin
SERVER   := $(BIN_DIR)/kb-server
CLI      := $(BIN_DIR)/kb
MCP      := $(BIN_DIR)/kb-mcp

build: $(SERVER) $(CLI) $(MCP)

$(SERVER): $(shell find . -name '*.go') go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $@ ./cmd/kb-server

$(CLI): $(shell find . -name '*.go') go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $@ ./cmd/kb

$(MCP): $(shell find . -name '*.go') go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $@ ./cmd/kb-mcp

test:
	$(GO) test $(GOFLAGS) ./...

test-race:
	$(GO) test $(GOFLAGS) -race ./...

test-cover:
	$(GO) test $(GOFLAGS) -coverpkg=./internal/... -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

# Verifies the internal/** coverage floor from design.md §19.
#
# Policy: 100% line coverage on internal/** modulo explicit exceptions
# documented in docs/coverage-exceptions.md (defensive guards against SQL
# driver-level faults that cannot be triggered without a fault-injecting
# wrapper). We assert a 97% floor on the total here.
test-cover-strict: test-cover
	@total=$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {print $$NF}' | tr -d '%'); \
	floor=97.0; \
	awk -v t="$$total" -v f="$$floor" 'BEGIN{ if (t+0 < f+0) {print "coverage", t"% <", f"% floor"; exit 1} print "coverage", t"% >=", f"% floor (see docs/coverage-exceptions.md)"; }'

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet $(GOFLAGS) ./...

tidy:
	$(GO) mod tidy

run: build
	./$(SERVER)

clean:
	rm -rf $(BIN_DIR) coverage.out kb.db kb.db-journal kb.db-wal kb.db-shm

openapi-validate:
	@command -v swagger-cli >/dev/null 2>&1 || { echo "install swagger-cli for validation"; exit 0; }
	swagger-cli validate api/openapi.yaml
