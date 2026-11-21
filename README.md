# gpu-tray-mon

A tiny Windows system-tray GPU monitor, written in pure Go (no CGO).

For each GPU adapter it shows discrete tray icons:

- **utilization** - a sparkline chart, like Task Manager's GPU graph
- **memory** - two stacked battery bars: dedicated (VRAM) vs shared (system) usage

Hover an icon for the adapter name and exact numbers; right-click for
**Pause monitoring / Resume monitoring / Quit**.

## Install

```powershell
go install -ldflags "-H windowsgui" github.com/dhiltgen/gpu-tray-mon@latest
```

This drops a windowless `gpu-tray-mon.exe` into `$(go env GOPATH)\bin`.
Omit `-ldflags "-H windowsgui"` for a console build (opens a console window;
useful for `-dump` diagnostics).

## Run at login

```powershell
gpu-tray-mon.exe --install      # register a login item
gpu-tray-mon.exe --uninstall    # remove it
```

Run it once (or log out/in). New tray icons appear in the notification
overflow (the `^` chevron next to the clock); drag them onto the taskbar to pin
them visible.

## Run

```sh
gpu-tray-mon.exe
```

```sh
# headless: print per-adapter stats once a second
gpu-tray-mon.exe -dump
gpu-tray-mon.exe -dump -interval 500ms
```

## Troubleshooting

If the tray shows a red **X** ("No GPU detected"), or a GPU you know is
present isn't showing up, run the diagnostic from a console:

```sh
go run . -dump
```

`-dump` prints the full display-adapter enumeration - every adapter Windows
reports, whether it's chartable, and why it was skipped - plus any PDH counter
errors. Common causes of "GPU present but not detected":

- the adapter has **no WDDM driver loaded** (check Device Manager),
- you're in a **remote/headless session** (RDP) where the display adapter is
  hidden,
- the GPU is **compute-only** with no display role - gpu-tray-mon charts display
  adapters, so a headless compute card won't appear,
- **Windows 10+ with a WDDM 2.x driver** is required for the GPU performance
  counters.

Note: `-dump` writes to stdout, so it needs a console. The `-H windowsgui`
release build drops its output - use `go run . -dump` or a plain `go build`
(dev) binary for diagnostics.

## Build

```sh
# dev build (console attached, useful for -dump)
go build -o gpu-tray-mon.exe .

# release build (no console window)
go build -ldflags "-H windowsgui" -o gpu-tray-mon.exe .
```

## Design

- **Pure Go**, `golang.org/x/sys/windows` only - PDH, D3DKMT, Shell_NotifyIcon,
  and GDI all called via `syscall.NewLazyDLL`.
- **Vendor-agnostic** backend: Windows PDH performance counters
  (`GPU Engine`, `GPU Adapter Memory`), so it works on NVIDIA, AMD, and Intel.
  Adapters are identified via D3DKMT (name + memory totals), and non-display
  compute adapters (e.g. an NPU) plus software render adapters are excluded.
- **One color per GPU**; metric is told apart by icon shape (sparkline vs
  battery bars). No load-based color shifting - 100% utilization is a good
  thing here.
- **Default 1 s** sampling; cheap enough to be invisible. Tune with `-interval`.
- Icons render at the DPI-scaled tray size and are re-added automatically if
  Explorer restarts.

## Status

Complete for v1: per-GPU utilization + memory icons, hover tooltips,
right-click pause/quit, a config file (`--config`), autostart
(`--install`/`--uninstall`), a single-instance guard, DPI-aware sizing,
Explorer-restart recovery, no-GPU diagnostics, and an MIT license.
