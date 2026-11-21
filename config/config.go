// Package config loads and writes the user-editable gpu-tray-mon config
// (colors + refresh interval) from %APPDATA%\gpu-tray-mon\config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config is the user-editable configuration.
type Config struct {
	Interval string   `json:"interval,omitempty"` // duration string, e.g. "1s"
	Colors   []string `json:"colors,omitempty"`   // hex "#RRGGBB", one per GPU
}

const defaultInterval = "1s"

var defaultColors = []string{
	"#4fc3f7", "#ffb74d", "#81c784", "#e57373",
	"#ba68c8", "#ffd54f", "#4dd0e1", "#f06292",
}

// Default returns the built-in configuration.
func Default() Config {
	colors := make([]string, len(defaultColors))
	copy(colors, defaultColors)
	return Config{Interval: defaultInterval, Colors: colors}
}

// Path returns the config file path (%APPDATA%\gpu-tray-mon\config.json).
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gpu-tray-mon", "config.json"), nil
}

// Load reads the config file, or returns the OS error if absent.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

// Ensure creates the config file (with defaults) if it doesn't exist, then
// returns the config read from disk.
func Ensure(path string) (Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Config{}, err
		}
		if err := Write(path, Default()); err != nil {
			return Config{}, err
		}
	}
	return Load(path)
}

// Write marshals c (pretty-printed) to path.
func Write(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// IntervalDuration parses the interval duration string, falling back to 1s.
func (c Config) IntervalDuration() time.Duration {
	if c.Interval == "" {
		return time.Second
	}
	d, err := time.ParseDuration(c.Interval)
	if err != nil || d <= 0 {
		return time.Second
	}
	return d
}

// Palette parses the color list into RGBA values, falling back to the
// built-in palette (or a single accent color) when none are valid.
func (c Config) Palette() []color.RGBA {
	if len(c.Colors) == 0 {
		return parseColors(defaultColors)
	}
	return parseColors(c.Colors)
}

func parseColors(hexes []string) []color.RGBA {
	out := make([]color.RGBA, 0, len(hexes))
	for _, h := range hexes {
		if c, ok := parseHex(h); ok {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return []color.RGBA{{R: 0x4f, G: 0xc3, B: 0xf7, A: 0xff}}
	}
	return out
}

func parseHex(s string) (color.RGBA, bool) {
	if len(s) != 7 || s[0] != '#' {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}
