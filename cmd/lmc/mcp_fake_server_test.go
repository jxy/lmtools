package main

import (
	"lmtools/internal/mcp/mcptest"
	"os"
	"testing"
)

// sharedTestDirs are directories that outlive a single test, such as the one
// holding the lmc binary the integration tests build. TestMain removes them
// once every test has run, since t.TempDir would remove them after the first.
var sharedTestDirs []string

// TestMain lets this test binary be the stdio MCP server an lmc under test
// launches: with the scenario variable set it serves and exits before any
// test runs. The integration tests name it as the server command.
func TestMain(m *testing.M) {
	mcptest.RunFromEnv()
	code := m.Run()
	for _, dir := range sharedTestDirs {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}
