.PHONY: build build-all build-go-only build-frontend clean test vet lint

BINARY=murmur
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

build: build-frontend build-go-only

build-go-only:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/murmur

build-frontend:
	@if command -v pnpm >/dev/null 2>&1; then \
		cd web/frontend && pnpm install --frozen-lockfile && pnpm run build; \
	else \
		echo "pnpm not found — skipping frontend build (using existing web/dist)"; \
	fi

build-all: build-frontend
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-amd64 ./cmd/murmur
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 ./cmd/murmur
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-darwin-arm64 ./cmd/murmur
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-windows-amd64.exe ./cmd/murmur

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/
	rm -rf web/dist
	mkdir -p web/dist
	touch web/dist/.gitkeep
