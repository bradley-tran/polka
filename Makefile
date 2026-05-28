APP_NAME   := polka
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -ldflags "\
  -X github.com/git@github.com/cmd.AppVersion=$(VERSION) \
  -X github.com/git@github.com/cmd.CommitHash=$(COMMIT) \
  -X github.com/git@github.com/cmd.BuildDate=$(BUILD_DATE)"

.PHONY: build run test lint clean

build:
	go build $(LDFLAGS) -o $(APP_NAME) .

run:
	go run $(LDFLAGS) . $(ARGS)

test:
	go test ./... -v -race -coverprofile=coverage.txt

lint:
	golangci-lint run ./...

clean:
	rm -f $(APP_NAME) coverage.txt
