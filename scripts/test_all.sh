#!/usr/bin/env bash

# Combined test script for API proxy and thinking field functionality

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}API Proxy Complete Test Suite${NC}"
echo "=============================="

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

# Test streaming
test_streaming() {
    echo -e "\n${YELLOW}Test 5: Streaming Response${NC}"
    echo "Testing streaming mode with Server-Sent Events"
    
    # Basic streaming test
    echo "Basic streaming test..."
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -H "Accept: text/event-stream" \
        -d '{
            "model": "claude-3-haiku-20240307",
            "max_tokens": 100,
            "stream": true,
            "messages": [
                {"role": "user", "content": "Count from 1 to 5 slowly"}
            ]
        }' | head -20
    
    echo -e "\n\nStreaming with tools test..."
    # Streaming with tools
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -H "Accept: text/event-stream" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 200,
            "stream": true,
            "messages": [
                {"role": "user", "content": "What is the weather in Paris? Use the weather tool."}
            ],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state/country, e.g. San Francisco, CA"
                            }
                        },
                        "required": ["location"]
                    }
                }
            ]
        }' | head -30
}

# Test with tools
test_tools() {
    echo -e "\n${YELLOW}Test 6: Tool Use (Anthropic Format)${NC}"
    echo "Testing with get_weather tool"
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "What is the weather like in San Francisco?"}
            ],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state, e.g. San Francisco, CA"
                            },
                            "unit": {
                                "type": "string",
                                "enum": ["celsius", "fahrenheit"],
                                "description": "The unit of temperature, either celsius or fahrenheit"
                            }
                        },
                        "required": ["location"]
                    }
                }
            ]
        }' | jq .
}

# Test tool use with user response
test_tool_use_response() {
    echo -e "\n${YELLOW}Test 7: Tool Use with User Response (Full Flow)${NC}"
    echo "Testing complete tool use flow with Anthropic API format"
    
    # Step 1: Initial request that should trigger tool use
    echo "Step 1: Initial request that triggers tool use..."
    RESPONSE=$(curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "What is the current weather in New York, NY?"}
            ],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state, e.g. San Francisco, CA"
                            },
                            "unit": {
                                "type": "string",
                                "enum": ["celsius", "fahrenheit"],
                                "description": "The unit of temperature"
                            }
                        },
                        "required": ["location"]
                    }
                }
            ]
        }')
    
    echo "$RESPONSE" | jq .
    
    # Extract tool_use_id from response (this is a simplified example)
    # In a real implementation, you would parse the JSON response
    
    # Step 2: Submit tool result back to Claude
    echo -e "\nStep 2: Submitting tool result back to Claude..."
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": "What is the current weather in New York, NY?"
                },
                {
                    "role": "assistant",
                    "content": [
                        {
                            "type": "text",
                            "text": "I'\''ll check the current weather in New York, NY for you."
                        },
                        {
                            "type": "tool_use",
                            "id": "toolu_01234567890abcdef",
                            "name": "get_weather",
                            "input": {
                                "location": "New York, NY",
                                "unit": "fahrenheit"
                            }
                        }
                    ]
                },
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_01234567890abcdef",
                            "content": "Temperature: 72°F, Conditions: Partly cloudy, Humidity: 65%"
                        }
                    ]
                }
            ],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state, e.g. San Francisco, CA"
                            },
                            "unit": {
                                "type": "string",
                                "enum": ["celsius", "fahrenheit"],
                                "description": "The unit of temperature"
                            }
                        },
                        "required": ["location"]
                    }
                }
            ]
        }' | jq .
}

# Test parallel tool use
test_parallel_tools() {
    echo -e "\n${YELLOW}Test 8: Parallel Tool Use${NC}"
    echo "Testing multiple tools called in parallel"
    
    # Step 1: Request that should trigger multiple tools
    echo "Step 1: Request triggering multiple tools..."
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "What is the weather like in both San Francisco and New York right now? Also what time is it in both cities?"}
            ],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state, e.g. San Francisco, CA"
                            }
                        },
                        "required": ["location"]
                    }
                },
                {
                    "name": "get_time",
                    "description": "Get the current time in a given timezone",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "timezone": {
                                "type": "string",
                                "description": "The IANA timezone name, e.g. America/Los_Angeles"
                            }
                        },
                        "required": ["timezone"]
                    }
                }
            ]
        }' | jq .
    
    # Step 2: Submit multiple tool results
    echo -e "\nStep 2: Submitting multiple tool results..."
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -d '{
            "model": "claude-3-sonnet-20240229",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": "What is the weather like in both San Francisco and New York right now? Also what time is it in both cities?"
                },
                {
                    "role": "assistant",
                    "content": [
                        {
                            "type": "text",
                            "text": "I'\''ll check the weather and time for both San Francisco and New York."
                        },
                        {
                            "type": "tool_use",
                            "id": "toolu_weather_sf",
                            "name": "get_weather",
                            "input": {"location": "San Francisco, CA"}
                        },
                        {
                            "type": "tool_use",
                            "id": "toolu_weather_ny",
                            "name": "get_weather",
                            "input": {"location": "New York, NY"}
                        },
                        {
                            "type": "tool_use",
                            "id": "toolu_time_sf",
                            "name": "get_time",
                            "input": {"timezone": "America/Los_Angeles"}
                        },
                        {
                            "type": "tool_use",
                            "id": "toolu_time_ny",
                            "name": "get_time",
                            "input": {"timezone": "America/New_York"}
                        }
                    ]
                },
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_weather_sf",
                            "content": "Temperature: 65°F, Conditions: Sunny, Humidity: 55%"
                        },
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_weather_ny",
                            "content": "Temperature: 72°F, Conditions: Partly cloudy, Humidity: 65%"
                        },
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_time_sf",
                            "content": "3:45 PM PST"
                        },
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_time_ny",
                            "content": "6:45 PM EST"
                        }
                    ]
                }
            ],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the current weather in a given location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "The city and state, e.g. San Francisco, CA"
                            }
                        },
                        "required": ["location"]
                    }
                },
                {
                    "name": "get_time",
                    "description": "Get the current time in a given timezone",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "timezone": {
                                "type": "string",
                                "description": "The IANA timezone name, e.g. America/Los_Angeles"
                            }
                        },
                        "required": ["timezone"]
                    }
                }
            ]
        }' | jq .
}

# Test thinking field - Claude model
test_thinking_claude() {
    echo -e "\n${YELLOW}Test 9: Claude Model with Thinking Enabled${NC}"
    echo "Testing thinking field support"
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-opus-4",
            "max_tokens": 100,
            "messages": [
                {"role": "user", "content": "What is 2+2?"}
            ],
            "thinking": {
                "type": "enabled",
                "budget_tokens": 31999
            }
        }' | jq .
}

# Test streaming with thinking
test_streaming_thinking() {
    echo -e "\n${YELLOW}Test 12: Streaming with Thinking Enabled${NC}"
    echo "Testing streaming mode with thinking field"
    
    curl -s -X POST http://localhost:8082/v1/messages \
        -H "Content-Type: application/json" \
        -H "anthropic-version: 2023-06-01" \
        -H "Accept: text/event-stream" \
        -d '{
            "model": "claude-opus-4",
            "max_tokens": 200,
            "stream": true,
            "messages": [
                {"role": "user", "content": "Explain why 2+2=4 in simple terms"}
            ],
            "thinking": {
                "type": "enabled",
                "budget_tokens": 5000
            }
        }' | head -40
}

# Main execution
check_proxy

echo -e "\n${BLUE}Running all tests...${NC}\n"

# Basic API tests
test_root
test_chat
test_system
test_token_count
test_streaming
test_tools
test_tool_use_response
test_parallel_tools

# Thinking field tests
echo -e "\n${BLUE}Testing thinking field functionality...${NC}"
test_thinking_claude
test_streaming_thinking

echo -e "\n${GREEN}All tests completed!${NC}"
echo -e "${YELLOW}Check the apiproxy logs for debug messages showing thinking field handling.${NC}"
