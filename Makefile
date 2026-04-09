.PHONY: build run test clean fmt lint vet everything

BINARY := vig
MODULE := github.com/kkurian/vig

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY)

test:
	go test ./...

fmt:
	go fmt ./...
	goimports -w .

lint:
	golangci-lint run ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

everything: fmt vet test build
	@echo "All checks passed."
