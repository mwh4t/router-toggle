-include .env

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.defaultAPI=$(API_URL)
RELFLAGS := $(LDFLAGS) -X main.version=$(VERSION)
BUILD   := go build -trimpath
CLIENT  := ./cmd/router-toggle
DIST    := dist

.PHONY: run client server release test clean

run:
	go run -ldflags "$(LDFLAGS)" $(CLIENT)

client:
	$(BUILD) -ldflags "$(LDFLAGS)" -o router-toggle $(CLIENT)

server:
	GOOS=linux GOARCH=amd64 $(BUILD) -o rt-server ./cmd/rt-server

test:
	go test ./...

release:
	@echo "version $(VERSION), api $(API_URL)"
	mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 $(BUILD) -ldflags "$(RELFLAGS)" -o $(DIST)/router-toggle-darwin-arm64      $(CLIENT)
	GOOS=darwin  GOARCH=amd64 $(BUILD) -ldflags "$(RELFLAGS)" -o $(DIST)/router-toggle-darwin-amd64      $(CLIENT)
	GOOS=windows GOARCH=amd64 $(BUILD) -ldflags "$(RELFLAGS)" -o $(DIST)/router-toggle-windows-amd64.exe $(CLIENT)
	GOOS=linux   GOARCH=amd64 $(BUILD) -ldflags "$(RELFLAGS)" -o $(DIST)/router-toggle-linux-amd64       $(CLIENT)
	cd $(DIST) && shasum -a 256 router-toggle-* > checksums.txt

clean:
	rm -rf $(DIST) router-toggle rt-server
