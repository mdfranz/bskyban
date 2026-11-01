BIN := bskyban

.PHONY: all build run test vet fmt tidy install clean help

all: fmt vet build ## format, vet, build

build: ## build ./bskyban
	go build -o $(BIN) .

run: build ## launch the TUI (ARGS="--since 10")
	./$(BIN) review $(ARGS)

test: ## run tests
	go test ./...

vet: ## go vet
	go vet ./...

fmt: ## gofmt all files
	gofmt -w *.go

tidy: ## go mod tidy
	go mod tidy

install: ## install to $GOBIN / $GOPATH/bin
	go install .

clean: ## remove built binary
	rm -f $(BIN)

help: ## list targets
	@grep -E '^[a-z]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'
