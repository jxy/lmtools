SHELL := /bin/bash

.PHONY: lint lint-fix test build

lint:
	golangci-lint run

lint-fix:
	golangci-lint run --fix

test:
	go test ./argolib ./cmd/argo

build:
	go build -o ./bin/argo ./cmd/argo
