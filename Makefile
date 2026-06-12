.PHONY: build build-amd build-windows test clean

build:
	go build -trimpath -ldflags '-s -w' -o bqagent ./cmd/agent

build-amd:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o bqagent ./cmd/agent

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o bqagent.exe ./cmd/agent

test:
	go test ./...

clean:
	rm -f bqagent bqagent.exe
