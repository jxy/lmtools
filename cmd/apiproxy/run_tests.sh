#!/bin/bash

# Test runner for API proxy

set -e

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}API Proxy Test Runner${NC}"
echo "====================="

# Function to run tests with a specific tag
run_tests() {
    local tag=$1
    local desc=$2
    echo -e "\n${YELLOW}Running ${desc}...${NC}"
    
    if go test -tags="${tag}" -v ../../internal/apiproxy/...; then
        echo -e "${GREEN}✓ ${desc} passed${NC}"
        return 0
    else
        echo -e "${RED}✗ ${desc} failed${NC}"
        return 1
    fi
}

# Check Go installation
echo -n "Checking Go installation... "
if command -v go &> /dev/null; then
    echo -e "${GREEN}✓${NC} $(go version)"
else
    echo -e "${RED}✗${NC}"
    echo "Go is not installed. Please install Go first."
    exit 1
fi

# Change to script directory
cd "$(dirname "$0")"

# Run different test suites
failed=0

# Unit tests (always run)
if ! run_tests "" "Unit Tests"; then
    ((failed++))
fi

# Integration tests
if ! run_tests "integration" "Integration Tests"; then
    ((failed++))
fi

# E2E tests
if ! run_tests "e2e" "End-to-End Tests"; then
    ((failed++))
fi

# Coverage report
echo -e "\n${YELLOW}Generating coverage report...${NC}"
go test -coverprofile=coverage.out ../../internal/apiproxy/...
go tool cover -html=coverage.out -o coverage.html
echo -e "${GREEN}✓ Coverage report saved to coverage.html${NC}"

# Performance benchmarks (optional)
if [[ "$1" == "--bench" ]]; then
    echo -e "\n${YELLOW}Running benchmarks...${NC}"
    go test -bench=. -benchmem ../../internal/apiproxy/...
fi

# Real API tests (optional)
if [[ "$E2E_REAL_APIS" == "true" ]]; then
    echo -e "\n${YELLOW}Running real API tests...${NC}"
    if ! run_tests "e2e" "Real API Tests"; then
        ((failed++))
    fi
fi

# Summary
echo -e "\n${BLUE}Test Summary${NC}"
echo "============"
if [[ $failed -eq 0 ]]; then
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "${RED}${failed} test suite(s) failed${NC}"
    exit 1
fi