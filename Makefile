.PHONY: build-server build-client build test clean

build: build-server build-client

build-server:
	go build -o bin/soralink-server ./cmd/soralink-server

build-client:
	go build -o bin/soralink-client ./cmd/soralink-client

build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/soralink-server-linux ./cmd/soralink-server

test:
	go test -v -race ./...

clean:
	rm -rf bin/
