.PHONY: build test install

build:
	go build -o bin/warp-agent-session-restore .

test:
	go test -race ./...
	go vet ./...

install: build
	./bin/warp-agent-session-restore install
