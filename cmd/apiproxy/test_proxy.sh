#!/bin/bash

# Test script for API proxy

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}API Proxy Test Suite${NC}"
echo "===================="

# Check if proxy is running
check_proxy() {
    echo -n "Checking if proxy is running on port 8082... "
    if curl -s http://localhost:8082/ > /dev/null; then
        echo -e "${GREEN}✓${NC}"
        return 0
    else
        echo -e "${RED}✗${NC}"
        echo "Please start the proxy first: ./apiproxy"
        exit 1
    fi
}

# Test root endpoint
test_root() {
    echo -e "\n${YELLOW}Test 1: Root Endpoint${NC}"
    echo "Testing GET /"
    curl -s http://localhost:8082/ | jq .
}

# Test simple chat
test_chat() {
    echo -e "\n${YELLOW}Test 2: Simple Chat Completion${NC}"
    echo "Testing POST /v1/messages"
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-3-haiku-20240307",
            "max_tokens": 100,
            "messages": [
                {"role": "user", "content": "Say hello in exactly 3 words"}
            ]
        }' | jq .
}

# Test with system message
test_system() {
    echo -e "\n${YELLOW}Test 3: Chat with System Message${NC}"
    echo "Testing with system prompt"
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 150,
            "system": "You are a pirate. Always speak like a pirate.",
            "messages": [
                {"role": "user", "content": "Tell me about the weather"}
            ]
        }' | jq .
}

# Test token counting
test_token_count() {
    echo -e "\n${YELLOW}Test 4: Token Counting${NC}"
    echo "Testing POST /v1/messages/count_tokens"
    curl -s -X POST http://localhost:8082/v1/messages/count_tokens \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-3-haiku-20240307",
            "messages": [
                {"role": "user", "content": "This is a test message to count tokens. How many tokens does this message contain?"}
            ]
        }' | jq .
}

# Test streaming (if implemented)
test_streaming() {
    echo -e "\n${YELLOW}Test 5: Streaming Response${NC}"
    echo "Testing streaming mode"
    echo -n "Streaming response: "
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-3-haiku-20240307",
            "max_tokens": 50,
            "stream": true,
            "messages": [
                {"role": "user", "content": "Count from 1 to 5"}
            ]
        }'
    echo # New line after streaming
}

# Test with tools
test_tools() {
    echo -e "\n${YELLOW}Test 6: Tool Use${NC}"
    echo "Testing with calculator tool"
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 200,
            "messages": [
                {"role": "user", "content": "What is 25 * 4?"}
            ],
            "tools": [
                {
                    "name": "calculator",
                    "description": "A simple calculator",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "expression": {
                                "type": "string",
                                "description": "Mathematical expression to evaluate"
                            }
                        },
                        "required": ["expression"]
                    }
                }
            ]
        }' | jq .
}

# Run tests
check_proxy

echo -e "\n${BLUE}Running tests...${NC}\n"

test_root
test_chat
test_system
test_token_count
# test_streaming  # Uncomment when ready to test streaming
test_tools

echo -e "\n${GREEN}All tests completed!${NC}"