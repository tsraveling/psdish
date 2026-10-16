package psd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

const fixture = "../test/test.psd"

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Skip("fixture missing:", fixture)
	}
	return b
}

func TestRoundtripIdentical(t *testing.T) {
	b := loadFixture(t)
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	out := f.Bytes()
	if !bytes.Equal(b, out) {
		n := 0
		for n < len(b) && n < len(out) && b[n] == out[n] {
			n++
		}
		t.Fatalf("roundtrip differs: in=%d out=%d first diff at %d", len(b), len(out), n)
	}
}

func header(ver, depth, mode uint16) []byte {
	b := []byte("8BPS")
	b = binary.BigEndian.AppendUint16(b, ver)
	b = append(b, make([]byte, 6)...)
	b = binary.BigEndian.AppendUint16(b, 3)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = binary.BigEndian.AppendUint16(b, depth)
	b = binary.BigEndian.AppendUint16(b, mode)
	return append(b, make([]byte, 12)...) // empty color data, resources, layers
}

func TestRejectsUnsupported(t *testing.T) {
	for name, b := range map[string][]byte{
		"psb":    header(2, 8, ModeRGB),
		"16-bit": header(1, 16, ModeRGB),
		"cmyk":   header(1, 8, 4),
	} {
		if _, err := Parse(b); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := Parse([]byte("nope")); err == nil {
		t.Error("garbage should fail")
	}
}

func TestMinimalNoLayersRoundtrip(t *testing.T) {
	b := append(header(1, 8, ModeRGB), 0, 0, 1, 2, 3)
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Bytes(), b) {
		t.Fatal("no-layer file did not roundtrip")
	}
	if d := NewDoc(f); len(d.Nodes()) != 0 {
		t.Fatal("expected empty tree")
	}
}
