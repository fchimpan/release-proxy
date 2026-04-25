.PHONY: build test lint run docker-build docker-run tidy

build:
	go build -o bin/release-proxy .

test:
	go test -v -race ./...

lint:
	go vet ./...
	golangci-lint run

run:
	go run .

docker-build:
	docker build -t release-proxy .

docker-run:
	docker run -p 8080:8080 release-proxy

tidy:
	go mod tidy
