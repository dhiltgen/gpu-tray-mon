package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseHex(t *testing.T) {
	c, ok := parseHex("#4fc3f7")
	if !ok || c.R != 0x4f || c.G != 0xc3 || c.B != 0xf7 || c.A != 0xff {
		t.Fatalf("parseHex(#4fc3f7) = %v, %v", c, ok)
	}
	for _, bad := range []string{"4fc3f7", "#zzzzzz", "#4fc3f", "#4fc3f77", ""} {
		if _, ok := parseHex(bad); ok {
			t.Errorf("parseHex(%q) should fail", bad)
		}
	}
}

func TestPalette(t *testing.T) {
	p := (Config{Colors: []string{"#ff0000", "#00ff00"}}).Palette()
	if len(p) != 2 || p[0].R != 0xff || p[1].G != 0xff {
		t.Fatalf("palette = %v", p)
	}
	if got := len((Config{}).Palette()); got != len(defaultColors) {
		t.Fatalf("empty palette len = %d, want %d", got, len(defaultColors))
	}
	if got := len((Config{Colors: []string{"nope"}}).Palette()); got != 1 {
		t.Fatalf("all-invalid palette len = %d, want 1", got)
	}
}

func TestInterval(t *testing.T) {
	if got := (Config{}).IntervalDuration(); got != time.Second {
		t.Errorf("empty interval = %v", got)
	}
	if got := (Config{Interval: "500ms"}).IntervalDuration(); got != 500*time.Millisecond {
		t.Errorf("500ms interval = %v", got)
	}
	if got := (Config{Interval: "bogus"}).IntervalDuration(); got != time.Second {
		t.Errorf("invalid interval = %v", got)
	}
	if got := (Config{Interval: "-5s"}).IntervalDuration(); got != time.Second {
		t.Errorf("negative interval = %v", got)
	}
}

func TestEnsureCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if _, err := Load(path); err == nil {
		t.Fatal("Load should fail before Ensure")
	}
	if _, err := Ensure(path); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Ensure: %v", err)
	}
	if cfg.Interval != defaultInterval {
		t.Errorf("defaults not written: interval = %q", cfg.Interval)
	}
}
