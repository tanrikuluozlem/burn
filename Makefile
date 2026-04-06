VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint docker clean

build:
	CGO_ENABLED=0 go build -ldflags="-w -s -X main.version=$(VERSION)" -o burn ./cmd/burn

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run

docker:
	docker build -t burn:$(VERSION) .

clean:
	rm -f burn coverage.out
