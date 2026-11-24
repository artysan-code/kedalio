package main

import (
	"fmt"
	"io"
)

// version is set at build time (-ldflags "-X main.version=1.2.3": the release workflow and the image); a plain
// `go build` says dev.
var version = "dev"

// printVersion answers `kedalio-core -version` (or --version) given alone, and reports whether it did, so that main
// does not start the core.
func printVersion(args []string, w io.Writer) bool {
	if len(args) != 1 || (args[0] != "-version" && args[0] != "--version") {
		return false
	}
	fmt.Fprintf(w, "kedalio-core %s\n", version)
	return true
}
