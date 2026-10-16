package main

const usage = `psdish — view and rearrange Photoshop (.psd) layer structure

Edits layer order, grouping, names and visibility without touching pixel data.
Supports 8-bit RGB and grayscale PSDs (no CMYK, 16/32-bit, or PSB).

USAGE
  psdish FILE.psd                  open the interactive TUI
  psdish FILE.psd -c [--json]      print the numbered layer hierarchy
  psdish FILE.psd OPS...           apply ops in order, save once, print new listing

TUI
  Left pane: layer tree. Right (or bottom, in tall panes): preview of the
  selection — the layer, a group with its visible children, or the whole
  document when the filename row is selected. "approx" marks previews that
  ignore effects/blend modes Photoshop would apply. Images use the kitty
  graphics protocol (Ghostty, kitty); other terminals get a half-block preview.

  navigation
    j/k, up/down      move cursor
    h, left           go to parent (top level → filename row)
    l, right          go to first child (expands a collapsed group)
    tab               collapse/expand group (view only, not saved)

  editing
    space             toggle visibility
    r                 rename
    J / K             move down / up within the group
    L                 move into the group directly above (at its bottom)
    H                 move out of the group, directly below it
    g                 wrap selection in a new group
    d                 delete (groups include all children)
    u / U, ctrl+r     undo / redo
    e                 export selection as PNG next to the psd

  files
    ctrl+s            save
    ctrl+a            save as (prompts before overwriting, even the same name)
    q, ctrl+c         quit (asks if there are unsaved changes)

CLI
  Listing lines read: LINE [!] on|off TYPE NAME, indented by depth.
  "!" marks special layers (text, smart object, adjustment, fill) that
  usually need flattening in Photoshop.

  REF is a line number from the listing, a path (Group/Sub/Layer), or a unique
  name. All REFs and LINEs resolve against the listing as it was before any op
  ran, so they always match the output you last saw.

  ops
    --move REF LINE [--into]  move before the item on LINE, adopting its parent;
                              --into makes it the last child of group LINE;
                              LINE = count+1 moves to the bottom of the top level
    --rename REF NAME
    --hide REF
    --show REF
    --toggle REF
    --delete REF              groups are deleted with all their children
    --group REF NAME          wrap REF in a new group called NAME
    --indent REF              move into the group directly above
    --outdent REF             move out of its group, directly below it
    --extract REF [OUT.png]   export as PNG next to the psd (default NAME.png)

  Ops that would do nothing (e.g. --indent with no group above) are skipped
  with a note on stderr. Any error aborts before saving and exits non-zero.

FLAGS
  -c, --cli          CLI mode (implied by any op)
  --json             JSON listing (line, name, path, type, depth, parent, ...)
  -o, --out PATH     write edits to PATH instead of in place
  --no-backup        don't write FILE.psd.bak before the first overwrite
  -f, --force        overwrite existing PNG exports
  --plain            TUI: plain glyphs instead of Nerd Font icons
  -h, --help         show this help
  -v, --version      show version

SAVING
  Saves go to a temp file, are re-read to verify, then replace the target.
  The first save over an existing file keeps a FILE.psd.bak copy. Photoshop's
  stored composite is marked stale when layer order or visibility changes;
  Photoshop rebuilds it on open.

ENVIRONMENT
  PSDISH_PLAIN=1             same as --plain
  PSDISH_GRAPHICS=kitty      force kitty image previews
  PSDISH_GRAPHICS=blocks     force half-block previews

EXAMPLES
  psdish hero.psd -c
  psdish hero.psd --json -c | jq '.layers[] | select(.special)'
  psdish hero.psd --move Tail 1 --hide "Brlink UP" --rename 3 "Lid Left"
  psdish hero.psd --group Nose NoseGroup -o hero-grouped.psd
  psdish hero.psd --extract Eyes eyes.png --force
`
