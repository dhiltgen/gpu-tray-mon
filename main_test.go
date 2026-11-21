//go:build windows

package main

import (
	"image/color"
	"testing"
)

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 KiB"},
		{1024, "1 KiB"},
		{2048, "2 KiB"},
		{1 << 20, "1 MiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, c := range cases {
		if got := bytes(c.in); got != c.want {
			t.Errorf("bytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFrac(t *testing.T) {
	if got := frac(5, 10); got != 0.5 {
		t.Errorf("frac(5,10) = %v", got)
	}
	if got := frac(3, 0); got != 0 {
		t.Errorf("frac(3,0) = %v", got)
	}
	if got := frac(0, 0); got != 0 {
		t.Errorf("frac(0,0) = %v", got)
	}
}

func TestColorFor(t *testing.T) {
	pal := []color.RGBA{{R: 1}, {R: 2}, {R: 3}}
	if got := colorFor(pal, 0).R; got != 1 {
		t.Errorf("colorFor(0) = %d", got)
	}
	if got := colorFor(pal, 3).R; got != 1 {
		t.Errorf("colorFor(3) should wrap to 0, got %d", got)
	}
	if got := colorFor(nil, 0).R; got != 0x4f {
		t.Errorf("colorFor(nil) fallback R = %d", got)
	}
}
