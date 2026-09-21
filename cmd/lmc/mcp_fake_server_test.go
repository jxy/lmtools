package main

import (
	"lmtools/internal/mcp/mcptest"
	"os"
	"testing"
)

// TestMain lets this test binary be the stdio MCP server an lmc under test
// launches: with the scenario variable set it serves and exits before any
// test runs. The integration tests name it as the server command.
func TestMain(m *testing.M) {
	mcptest.RunFromEnv()
	os.Exit(m.Run())
}
