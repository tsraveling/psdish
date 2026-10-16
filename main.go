package main

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
)

func main() {
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "psdish:", err)
		fmt.Fprintln(os.Stderr, "run 'psdish --help' for usage")
		os.Exit(2)
	}
	switch {
	case o.help:
		fmt.Print(usage)
		return
	case o.version:
		fmt.Println(Version)
		return
	case o.file == "":
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if o.cli {
		os.Exit(runCLI(o, os.Stdout, os.Stderr))
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(os.Stderr, "psdish: not a terminal; use -c for CLI mode")
		os.Exit(2)
	}
	os.Exit(runTUI(o))
}
