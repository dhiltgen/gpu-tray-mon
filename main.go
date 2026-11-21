//go:build windows

// Command gpu-tray-mon shows GPU utilization and memory in the Windows
// system tray. -dump prints per-adapter stats and diagnostics to stdout.
package main

import (
	"flag"
	"fmt"
	"image/color"
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/dhiltgen/gpu-tray-mon/adapter"
	"github.com/dhiltgen/gpu-tray-mon/autostart"
	"github.com/dhiltgen/gpu-tray-mon/config"
	"github.com/dhiltgen/gpu-tray-mon/pdh"
	"github.com/dhiltgen/gpu-tray-mon/tray"

	"golang.org/x/sys/windows"
)

var (
	libkernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateMutex = libkernel32.NewProc("CreateMutexW")
	procFreeConsole = libkernel32.NewProc("FreeConsole")
)

func colorFor(palette []color.RGBA, i int) color.RGBA {
	if len(palette) == 0 {
		return color.RGBA{R: 0x4f, G: 0xc3, B: 0xf7, A: 0xff}
	}
	return palette[i%len(palette)]
}

func main() {
	dump := flag.Bool("dump", false, "print per-adapter stats and diagnostics to stdout")
	intervalFlag := flag.Duration("interval", 0, "sampling interval (0 = use config, then 1s)")
	showConfig := flag.Bool("config", false, "create the config file if needed, print its path and contents, then exit")
	install := flag.Bool("install", false, "register as a login item and exit")
	uninstall := flag.Bool("uninstall", false, "remove the login item and exit")
	flag.Parse()

	switch {
	case *install:
		if err := autostart.Install(); err != nil {
			fmt.Fprintf(os.Stderr, "install: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("installed login item")
		return
	case *uninstall:
		if err := autostart.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("removed login item")
		return
	case *showConfig:
		path, err := config.Path()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config path: %v\n", err)
			os.Exit(1)
		}
		if _, err := config.Ensure(path); err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			os.Exit(1)
		}
		data, _ := os.ReadFile(path)
		fmt.Printf("config: %s\n%s", path, data)
		return
	}

	// Load config (built-in defaults if the file is absent or unreadable).
	cfg := config.Default()
	if path, err := config.Path(); err == nil {
		if loaded, err := config.Load(path); err == nil {
			cfg = loaded
		}
	}
	interval := cfg.IntervalDuration()
	if *intervalFlag > 0 {
		interval = *intervalFlag
	}
	palette := cfg.Palette()

	if !*dump {
		ok, err := singleInstance()
		if err != nil {
			fmt.Fprintf(os.Stderr, "single-instance: %v\n", err)
			os.Exit(1)
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "gpu-tray-mon is already running")
			os.Exit(1)
		}
	}

	// Diagnostics mode: print the full adapter enumeration first, then live
	// stats if any GPU is chartable.
	if *dump {
		details, err := adapter.EnumerateDetailed()
		if err != nil {
			fmt.Fprintf(os.Stderr, "adapters: %v\n", err)
			os.Exit(1)
		}
		printAdapterDetails(details)
		if countChartable(details) == 0 {
			os.Exit(1)
		}
		c, err := pdh.NewCollector()
		if err != nil {
			fmt.Fprintf(os.Stderr, "pdh: %v\n", err)
			os.Exit(1)
		}
		defer c.Close()
		runDump(c, details, interval)
		return
	}

	runTray(interval, palette)
}

// singleInstance reports whether this is the only running instance, using a
// named mutex. The mutex handle is intentionally never closed so it lives for
// the whole process lifetime.
func singleInstance() (bool, error) {
	name, err := windows.UTF16PtrFromString("gpu-tray-mon-single-instance")
	if err != nil {
		return false, err
	}
	handle, _, lastErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return false, fmt.Errorf("CreateMutex failed: %v", lastErr)
	}
	return lastErr != windows.ERROR_ALREADY_EXISTS, nil
}

func statsByLUID(stats []pdh.AdapterStats) map[string]pdh.AdapterStats {
	m := make(map[string]pdh.AdapterStats, len(stats))
	for _, s := range stats {
		m[s.LUID] = s
	}
	return m
}

func countChartable(details []adapter.Detail) int {
	n := 0
	for _, d := range details {
		if d.Chartable {
			n++
		}
	}
	return n
}

func printAdapterDetails(details []adapter.Detail) {
	fmt.Printf("display adapters: %d\n", len(details))
	for _, d := range details {
		status := "chartable"
		if !d.Chartable {
			status = "skipped: " + d.Reason
		}
		name := d.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("  %s  name=%q  dedicated_total=%d  [%s]\n", d.LUID, name, d.DedicatedTotalBytes, status)
	}
	if countChartable(details) == 0 {
		fmt.Println("no chartable GPU adapters detected. Possible causes:")
		fmt.Println("  - no display GPU installed, or its WDDM driver isn't loaded")
		fmt.Println("  - remote/headless session (RDP) hiding the display adapter")
		fmt.Println("  - a compute-only GPU with no display role (we only chart display adapters)")
		fmt.Println("  - Windows 10+ with a WDDM 2.x driver is required for the GPU counters")
	}
}

func runDump(c *pdh.Collector, details []adapter.Detail, interval time.Duration) {
	fmt.Println("time\tluid\tutil%\tdedicated_bytes\tshared_bytes")
	for {
		stats, err := c.Sample()
		if err != nil {
			fmt.Fprintf(os.Stderr, "sample: %v\n", err)
			time.Sleep(interval)
			continue
		}
		by := statsByLUID(stats)
		now := time.Now().Format("15:04:05")
		for _, d := range details {
			if !d.Chartable {
				continue
			}
			if s, ok := by[d.LUID]; ok {
				fmt.Printf("%s\t%s\t%.1f\t%d\t%d\n", now, d.LUID, s.Utilization, s.DedicatedBytes, s.SharedBytes)
			}
		}
		time.Sleep(interval)
	}
}

func runTray(interval time.Duration, palette []color.RGBA) {
	// Detach from the console on console-subsystem builds (go install/dev) so
	// no window lingers. No-op on a windowsgui build.
	procFreeConsole.Call()

	t, err := tray.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tray: %v\n", err)
		os.Exit(1)
	}

	adapters, err := adapter.Enumerate()
	if err != nil {
		runErrorTray(t, "GPU detection failed")
		return
	}
	if len(adapters) == 0 {
		runErrorTray(t, "No GPU detected")
		return
	}

	c, err := pdh.NewCollector()
	if err != nil {
		runErrorTray(t, "GPU metrics unavailable")
		return
	}
	defer c.Close()

	n := len(adapters)
	utilUID := make([]uint32, n)
	memUID := make([]uint32, n)
	for i := range adapters {
		utilUID[i] = uint32(2*i + 1)
		memUID[i] = uint32(2*i + 2)
		g1, err1 := tray.NewGUID()
		g2, err2 := tray.NewGUID()
		if err1 != nil || err2 != nil {
			fmt.Fprintln(os.Stderr, "guid:", err1, err2)
			os.Exit(1)
		}
		if err := t.AddIcon(utilUID[i], g1); err != nil {
			fmt.Fprintf(os.Stderr, "tray add (util %d): %v\n", i, err)
			os.Exit(1)
		}
		if err := t.AddIcon(memUID[i], g2); err != nil {
			fmt.Fprintf(os.Stderr, "tray add (mem %d): %v\n", i, err)
			os.Exit(1)
		}
	}

	removeAll := func() {
		for i := 0; i < n; i++ {
			t.RemoveIcon(utilUID[i])
			t.RemoveIcon(memUID[i])
		}
	}
	defer removeAll()

	var paused int32

	t.SetIconEvent(func(uID uint32, event uintptr) {
		if event != tray.EventRButtonUp && event != tray.EventContextMenu {
			return
		}
		pauseText := "Pause monitoring"
		if atomic.LoadInt32(&paused) != 0 {
			pauseText = "Resume monitoring"
		}
		cmd := t.ShowMenu([]tray.MenuItem{
			{ID: 1, Text: pauseText},
			{ID: 2, Text: "Quit"},
		})
		switch cmd {
		case 1:
			if atomic.LoadInt32(&paused) != 0 {
				atomic.StoreInt32(&paused, 0)
			} else {
				atomic.StoreInt32(&paused, 1)
			}
		case 2:
			removeAll()
			t.Stop()
		}
	})

	w, h := tray.IconSize()

	utilBuf := make([][]float64, n)
	for i := range utilBuf {
		utilBuf[i] = make([]float64, w)
	}

	name := func(i int) string {
		if adapters[i].Name != "" {
			return adapters[i].Name
		}
		return fmt.Sprintf("GPU %d", i)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if atomic.LoadInt32(&paused) != 0 {
				continue
			}
			stats, err := c.Sample()
			if err != nil {
				fmt.Fprintf(os.Stderr, "sample: %v\n", err)
				continue
			}
			by := statsByLUID(stats)
			for i, a := range adapters {
				s, ok := by[a.LUID]
				if !ok {
					continue
				}
				col := colorFor(palette, i)

				util := s.Utilization / 100.0
				copy(utilBuf[i], utilBuf[i][1:])
				utilBuf[i][len(utilBuf[i])-1] = util
				if err := t.SetIcon(utilUID[i], tray.RenderSparkline(utilBuf[i], col, w, h),
					fmt.Sprintf("%s: %.0f%%", name(i), s.Utilization)); err != nil {
					fmt.Fprintf(os.Stderr, "set util icon: %v\n", err)
				}

				imgM := tray.RenderMemoryBar(frac(s.DedicatedBytes, a.DedicatedTotalBytes),
					frac(s.SharedBytes, a.SharedTotalBytes), col, w, h)
				tipM := fmt.Sprintf("%s: VRAM %s/%s ded, %s shared",
					name(i), bytes(s.DedicatedBytes), bytes(a.DedicatedTotalBytes), bytes(s.SharedBytes))
				if err := t.SetIcon(memUID[i], imgM, tipM); err != nil {
					fmt.Fprintf(os.Stderr, "set mem icon: %v\n", err)
				}
			}
		}
	}()

	t.Run()
}

// runErrorTray shows a single red "X" icon with the given tooltip and a Quit
// menu, used when no GPU is detected or metrics can't be read.
func runErrorTray(t *tray.Tray, tip string) {
	const uID = 1
	guid, err := tray.NewGUID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "guid: %v\n", err)
		os.Exit(1)
	}
	if err := t.AddIcon(uID, guid); err != nil {
		fmt.Fprintf(os.Stderr, "tray add: %v\n", err)
		os.Exit(1)
	}
	defer t.RemoveIcon(uID)

	t.SetIconEvent(func(uID uint32, event uintptr) {
		if event != tray.EventRButtonUp && event != tray.EventContextMenu {
			return
		}
		if t.ShowMenu([]tray.MenuItem{{ID: 1, Text: "Quit"}}) == 1 {
			t.RemoveIcon(uID)
			t.Stop()
		}
	})

	w, h := tray.IconSize()
	img := tray.RenderX(color.RGBA{R: 0xff, G: 0x45, B: 0x45, A: 0xff}, w, h)
	if err := t.SetIcon(uID, img, tip); err != nil {
		fmt.Fprintf(os.Stderr, "set icon: %v\n", err)
	}

	t.Run()
}

func frac(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total)
}

func bytes(b int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%.0f MiB", float64(b)/mib)
	default:
		return fmt.Sprintf("%.0f KiB", float64(b)/kib)
	}
}
