// Command google-contacts-mcp is a remote MCP server exposing Google Contacts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/samber/google-contacts-mcp/internal/config"
	"github.com/samber/google-contacts-mcp/internal/crypto"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

const (
	cmdGenkey  = "genkey"
	cmdVersion = "version"

	// exitFailure is the conventional non-zero exit code for any error.
	exitFailure = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run dispatches the subcommand and returns the process exit code. A missing
// subcommand, or one starting with "-", means serve with those flags.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := config.CmdServe
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case cmdVersion:
		_, err := fmt.Fprintln(stdout, version)
		if err != nil {
			return exitFailure
		}
		return 0
	case cmdGenkey:
		// genkey never loads config: it creates the key the config needs.
		if len(args) > 0 {
			_, _ = fmt.Fprintf(stderr, "%s: unexpected arguments %q\n", cmd, args)
			return exitFailure
		}
		entry, err := crypto.GenerateKey()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return exitFailure
		}
		if _, err := fmt.Fprintln(stdout, entry); err != nil {
			return exitFailure
		}
		return 0
	case config.CmdServe, config.CmdRekey, config.CmdDisconnect:
		if _, err := config.Load(cmd, args, getenv); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				config.Usage(stdout)
				return 0
			}
			_, _ = fmt.Fprintln(stderr, err)
			return exitFailure
		}
		_, _ = fmt.Fprintf(stderr, "%s: not implemented\n", cmd)
		return exitFailure
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q (want %s, %s, %s, %s or %s)\n",
			cmd, config.CmdServe, cmdGenkey, config.CmdRekey, config.CmdDisconnect, cmdVersion)
		return exitFailure
	}
}
