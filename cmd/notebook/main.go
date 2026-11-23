// Command notebook serves a directory of Markdown notes over MCP stdio.
package main

import (
	"context"
	"flag"
	"fmt"
	"lmtools/internal/mcp"
	"lmtools/internal/notebook"
	"lmtools/internal/version"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", "", "directory containing the project notes")
	showVersion := flag.Bool("version", false, "print the build version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String("notebook"))
		return
	}
	if *root == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: notebook -root DIRECTORY")
		os.Exit(2)
	}
	dir, err := filepath.Abs(*root)
	if err == nil {
		err = mcp.ServeStdio(context.Background(), os.Stdin, os.Stdout, mcp.Implementation{Name: "notebook", Version: version.String("notebook")}, notebook.Instructions, notebook.Store{Root: dir})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
