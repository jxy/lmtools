.PHONY: build run clean test test-all test-unit test-integration test-e2e

# Binary name
BINARY=apiproxy

# Build the binary
build:
	go build -o $(BINARY) .

# Run the proxy
run: build
	./$(BINARY)

# Clean build artifacts
clean:
	rm -f $(BINARY)
	rm -f coverage.out coverage.html

# Run with example environment
run-example:
	@echo "Starting proxy with example configuration..."
	@echo "Please ensure you have set up your .env file based on .env.example"
	./$(BINARY)

# Test compilation
test:
	go build -o /dev/null .
	@echo "✓ Compilation successful"

# Run all tests
test-all: test-unit test-integration test-e2e

# Run unit tests
test-unit:
	@echo "Running unit tests..."
	go test -v ../../internal/apiproxy/...

# Run integration tests
test-integration:
	@echo "Running integration tests..."
	go test -tags=integration -v ../../internal/apiproxy/...

# Run e2e tests
test-e2e:
	@echo "Running e2e tests..."
	go test -tags=e2e -v ../../internal/apiproxy/...

# Generate coverage report
coverage:
	@echo "Generating coverage report..."
	go test -coverprofile=coverage.out ../../internal/apiproxy/...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report saved to coverage.html"

# Install globally
install:
	go install .

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY)-linux-amd64 .
	GOOS=darwin GOARCH=amd64 go build -o $(BINARY)-darwin-amd64 .
	GOOS=windows GOARCH=amd64 go build -o $(BINARY)-windows-amd64.exe .

# Example API test
test-api:
	@echo "Testing API endpoint..."
	curl -s http://localhost:8082/ | jq .

# Example chat request
test-chat:
	@echo "Testing chat completion..."
	curl -s -X POST http://localhost:8082/v1/messages \
		-H "Content-Type: application/json" \
		-d '{"model": "claude-3-haiku-20240307", "max_tokens": 100, "messages": [{"role": "user", "content": "Say hello"}]}' \
		| jq .