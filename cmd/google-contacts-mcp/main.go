// Command google-contacts-mcp is a remote MCP server exposing Google Contacts.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

const (
	cmdServe      = "serve"
	cmdGenkey     = "genkey"
	cmdRekey      = "rekey"
	cmdDisconnect = "disconnect"
	cmdVersion    = "version"

	// exitFailure is the conventional non-zero exit code for any error.
	exitFailure = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommand and returns the process exit code. A missing
// subcommand, or one starting with "-", means serve with those flags.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := cmdServe
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
	}

	switch cmd {
	case cmdVersion:
		_, err := fmt.Fprintln(stdout, version)
		if err != nil {
			return exitFailure
		}
		return 0
	case cmdServe, cmdGenkey, cmdRekey, cmdDisconnect:
		_, _ = fmt.Fprintf(stderr, "%s: not implemented\n", cmd)
		return exitFailure
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q (want %s, %s, %s, %s or %s)\n",
			cmd, cmdServe, cmdGenkey, cmdRekey, cmdDisconnect, cmdVersion)
		return exitFailure
	}
}
