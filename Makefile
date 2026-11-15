VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN ?= traefik-proxmenux-provider

.PHONY: build test vet fmt tidy clean

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/provider

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -f $(BIN)
