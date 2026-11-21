//go:build windows

// Package autostart registers/removes the login item that launches
// gpu-tray-mon at logon (HKCU Run key).
package autostart

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "gpu-tray-mon"
)

// Install writes a Run value pointing at the current executable.
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// Quote the path so spaces in the directory don't break the Run parser.
	return k.SetStringValue(valueName, `"`+exe+`"`)
}

// Uninstall removes the Run value. Idempotent: a missing value is not an error.
func Uninstall() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue(valueName)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}
