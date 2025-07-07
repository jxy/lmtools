SHELL := /bin/bash

.PHONY: lint test build

lint:
	golangci-lint run


test:
	go test ./argolib ./cmd/argo

build:
	go build ./cmd/argo