.PHONY: webui-deps webui-build build build-amd build-windows test eval eval-all clean

WEBUI_DIR := internal/server/webui
WEBUI_DEPS_STAMP := $(WEBUI_DIR)/node_modules/.package-lock.json

webui-deps: $(WEBUI_DEPS_STAMP)

$(WEBUI_DEPS_STAMP): $(WEBUI_DIR)/package.json $(WEBUI_DIR)/package-lock.json
	cd $(WEBUI_DIR) && npm ci

webui-build: webui-deps
	cd $(WEBUI_DIR) && npm run build

build: webui-build
	go build -trimpath -ldflags '-s -w' -o bqagent ./cmd/agent

build-amd: webui-build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o bqagent ./cmd/agent

build-windows: webui-build
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o bqagent.exe ./cmd/agent

test:
	go test ./...

eval:
	go run ./cmd/eval --suite smoke --mode replay

eval-all:
	go run ./cmd/eval --suite all --mode replay

clean:
	rm -f bqagent bqagent.exe
	rm -rf $(WEBUI_DIR)/dist
