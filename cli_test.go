package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "test/test.psd"

func fixtureCopy(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Skip("fixture missing:", fixture)
	}
	p := filepath.Join(t.TempDir(), "fixture.psd")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	o, err := parseArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	o.cli = true
	var out, errb bytes.Buffer
	code := runCLI(o, &out, &errb)
	return out.String(), errb.String(), code
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.txt")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update)", path)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch:\n%s", name, got)
	}
}

func TestCLIListing(t *testing.T) {
	p := fixtureCopy(t)
	out, _, code := run(t, p)
	if code != 0 {
		t.Fatal("exit", code)
	}
	golden(t, "listing", out)
}

func TestCLIEdits(t *testing.T) {
	p := fixtureCopy(t)
	out, errOut, code := run(t, p,
		"--move", "eyellids", "7", "--into",
		"--rename", "3", "Lid Left",
		"--hide", "head",
		"--group", "Nose", "NoseGroup",
		"--delete", "Brlink UP",
		"--move", "Tail", "1",
		"--indent", "Body",
	)
	if code != 0 {
		t.Fatal(errOut)
	}
	golden(t, "edits", out)
	// the saved file must list identically when re-read
	again, _, _ := run(t, p)
	if again != out {
		t.Fatal("re-read listing differs from post-edit listing")
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Fatal("no backup written")
	}
}

func TestCLIErrorsLeaveFileUntouched(t *testing.T) {
	p := fixtureCopy(t)
	before, _ := os.ReadFile(p)
	for _, args := range [][]string{
		{"--rename", "nope", "x"},         // missing
		{"--move", "1", "999"},            // out of range
		{"--move", "1", "2"},              // into own descendant
		{"--move", "Tail", "3", "--into"}, // not a group
		{"--delete", "1", "--hide", "2"},  // deleted by earlier op
	} {
		_, errOut, code := run(t, append([]string{p}, args...)...)
		if code == 0 || errOut == "" {
			t.Errorf("%v: expected failure", args)
		}
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("failed ops modified the file")
	}
}

func TestCLIExtract(t *testing.T) {
	p := fixtureCopy(t)
	_, errOut, code := run(t, p, "--extract", "Eyes", "--extract", "Tail", "tail out.png")
	if code != 0 {
		t.Fatal(errOut)
	}
	for _, n := range []string{"Eyes.png", "tail out.png"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), n)); err != nil {
			t.Error(n, err)
		}
	}
	if _, _, code := run(t, p, "--extract", "Eyes"); code == 0 {
		t.Error("overwrite without --force should fail")
	}
	if _, _, code := run(t, p, "--extract", "Eyes", "-f"); code != 0 {
		t.Error("--force should overwrite")
	}
}

func TestCLIOutLeavesOriginal(t *testing.T) {
	p := fixtureCopy(t)
	before, _ := os.ReadFile(p)
	out := filepath.Join(filepath.Dir(p), "copy.psd")
	if _, e, code := run(t, p, "--hide", "Tail", "-o", out); code != 0 {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("-o modified original")
	}
	listing, _, _ := run(t, out)
	if !strings.Contains(listing, "off layer        Tail") {
		t.Fatal("copy missing edit:\n" + listing)
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"a.psd", "--move", "x", "3", "--into", "--extract", "y", "o.png", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.cli || !o.json || len(o.ops) != 2 || !o.ops[0].into || o.ops[1].arg != "o.png" {
		t.Fatalf("%+v", o)
	}
	if _, err := parseArgs([]string{"a.psd", "--rename", "x"}); err == nil {
		t.Fatal("missing arg should error")
	}
	if _, err := parseArgs([]string{"a.psd", "--bogus"}); err == nil {
		t.Fatal("unknown flag should error")
	}
}
