.PHONY: build test lint

build:
	go build -o bin/foreman ./cmd/foreman

test:
	go test ./...

lint:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
