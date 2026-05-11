// Command mem-mcp is the MCP (Model Context Protocol) server for mem.
//
// W1 status: STUB. The real implementation lands in W4 (see SPEC §10.1).
// The binary builds and prints a friendly message so that downstream tooling
// (Claude Desktop, etc.) gets a clear "not yet" instead of a missing-binary
// error during early integration testing.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "mem-mcp: stub (W4 scope; not implemented yet)")
	fmt.Fprintln(os.Stderr, "See SPEC.md §8 for the planned tool surface.")
	if len(os.Args) > 1 && os.Args[1] == "--help" {
		fmt.Fprintln(os.Stderr, "Usage: mem mcp serve [--token <t>]   (coming in W4)")
		os.Exit(0)
	}
	os.Exit(0)
}
