package main

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
)

const usage = `psdish — view and rearrange Photoshop layer structure

usage:
  psdish FILE.psd                  open the TUI
  psdish FILE.psd -c [--json]      print the numbered layer hierarchy
  psdish FILE.psd [ops...]         apply ops in order, save once, print new listing

refs (REF) are a line number from the listing, a path (Group/Sub/Layer), or a
unique name. All refs and line numbers resolve against the listing as it was
before any op ran.

ops:
  --move REF LINE [--into]  move before the item on LINE (adopting its parent);
                            --into makes it the last child of group LINE.
                            LINE = count+1 moves to the bottom of the top level
  --rename REF NAME
  --hide REF | --show REF | --toggle REF
  --delete REF              deletes groups with all their children
  --group REF NAME          wrap REF in a new group
  --indent REF              move into the group directly above
  --outdent REF             move out of its group, directly below it
  --extract REF [OUT.png]   export as PNG next to the psd (default NAME.png)

flags:
  -c, --cli        CLI mode (implied by any op)
  --json           JSON listing output
  -o, --out PATH   write edits to PATH instead of in place
  --no-backup      skip FILE.psd.bak on first save
  -f, --force      overwrite existing PNG exports
  --plain          TUI: plain glyphs instead of Nerd Font icons (or PSDISH_PLAIN=1)
  -h, --help
  -v, --version
`

func main() {
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "psdish:", err)
		fmt.Fprint(os.Stderr, "\n"+usage)
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
