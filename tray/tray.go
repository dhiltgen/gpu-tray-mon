//go:build windows

// Package tray provides a minimal Windows system-tray icon: a hidden message
// window, Shell_NotifyIcon-based icons with dynamic updates, and an
// RGBA->HICON conversion path. No CGO, no third-party GUI dependencies.
package tray

import (
	"fmt"
	"image"
	"image/color"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Shell_NotifyIcon messages.
const (
	nimAdd        = 0x00000000
	nimModify     = 0x00000001
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
)

// NOTIFYICONDATA flags.
const (
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifGuid    = 0x00000020
	nifShowtip = 0x00000080
)

const notifyIconVersion4 = 4

// System metrics indices for the DPI-scaled small-icon (tray) size.
const (
	smCXSmallIcon = 0x31
	smCYSmallIcon = 0x32
)

// Window messages.
const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmApp         = 0x8000
	wmNull        = 0x0000
	wmContextMenu = 0x007B
)

// Tray icon mouse-event codes passed to the icon event handler (lParam).
const (
	EventMouseMove   = 0x0200
	EventLButtonUp   = 0x0202
	EventLButtonDbl  = 0x0203
	EventRButtonUp   = 0x0205
	EventContextMenu = 0x007B
)

// Popup menu flags.
const (
	mfString       = 0x0000
	tpmRightButton = 0x0002
	tpmNonotify    = 0x0080
	tpmReturnCmd   = 0x0100
)

var (
	libuser32   = windows.NewLazySystemDLL("user32.dll")
	libgdi32    = windows.NewLazySystemDLL("gdi32.dll")
	libshell32  = windows.NewLazySystemDLL("shell32.dll")
	libole32    = windows.NewLazySystemDLL("ole32.dll")
	libkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetModuleHandle       = libkernel32.NewProc("GetModuleHandleW")
	procRegisterClassEx       = libuser32.NewProc("RegisterClassExW")
	procCreateWindowEx        = libuser32.NewProc("CreateWindowExW")
	procDefWindowProc         = libuser32.NewProc("DefWindowProcW")
	procGetMessage            = libuser32.NewProc("GetMessageW")
	procTranslateMessage      = libuser32.NewProc("TranslateMessage")
	procDispatchMessage       = libuser32.NewProc("DispatchMessageW")
	procPostQuitMessage       = libuser32.NewProc("PostQuitMessage")
	procPostMessage           = libuser32.NewProc("PostMessageW")
	procShellNotifyIcon       = libshell32.NewProc("Shell_NotifyIconW")
	procCreateIconIndirect    = libuser32.NewProc("CreateIconIndirect")
	procCreateDIBSection      = libgdi32.NewProc("CreateDIBSection")
	procCreateBitmap          = libgdi32.NewProc("CreateBitmap")
	procDeleteObject          = libgdi32.NewProc("DeleteObject")
	procGetDC                 = libuser32.NewProc("GetDC")
	procReleaseDC             = libuser32.NewProc("ReleaseDC")
	procCoCreateGuid          = libole32.NewProc("CoCreateGuid")
	procCreatePopupMenu       = libuser32.NewProc("CreatePopupMenu")
	procAppendMenuW           = libuser32.NewProc("AppendMenuW")
	procTrackPopupMenu        = libuser32.NewProc("TrackPopupMenu")
	procDestroyMenu           = libuser32.NewProc("DestroyMenu")
	procGetCursorPos          = libuser32.NewProc("GetCursorPos")
	procSetForegroundWindow   = libuser32.NewProc("SetForegroundWindow")
	procGetSystemMetrics      = libuser32.NewProc("GetSystemMetrics")
	procRegisterWindowMessage = libuser32.NewProc("RegisterWindowMessageW")
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// notifyIconData mirrors NOTIFYICONDATAW (version 4 layout).
type notifyIconData struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeoutVersion  uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

type msg struct {
	hwnd    uintptr
	message uint32
	_       uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type point struct{ x, y int32 }

type iconInfo struct {
	fIcon    uint32
	xHotspot uint32
	yHotspot uint32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// IconEvent is a tray icon callback event (mouse message or balloon).
type IconEvent uintptr

type entry struct {
	guid  windows.GUID
	hicon windows.Handle
	tip   string
}

// Tray owns the hidden window and its icons.
type Tray struct {
	hwnd           windows.Handle
	callback       uint32
	taskbarCreated uint32
	mu             sync.Mutex
	icons          map[uint32]*entry
	onIconEvent    func(uID uint32, event uintptr)
}

var (
	activeMu sync.RWMutex
	active   = map[uintptr]*Tray{}
)

func registerTray(t *Tray) {
	activeMu.Lock()
	active[uintptr(t.hwnd)] = t
	activeMu.Unlock()
}

func unregisterTray(hwnd uintptr) {
	activeMu.Lock()
	delete(active, hwnd)
	activeMu.Unlock()
}

func trayFor(hwnd uintptr) *Tray {
	activeMu.RLock()
	t := active[hwnd]
	activeMu.RUnlock()
	return t
}

// wndProc is the window procedure for the hidden tray window.
func wndProc(hwnd uintptr, m uint32, wparam, lparam uintptr) uintptr {
	if m == wmDestroy {
		unregisterTray(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}
	t := trayFor(hwnd)
	if t != nil && t.taskbarCreated != 0 && m == t.taskbarCreated {
		t.reAddAll()
		return 0
	}
	if t != nil && t.callback != 0 && m == t.callback {
		if t.onIconEvent != nil {
			t.onIconEvent(uint32(wparam), lparam)
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(hwnd, uintptr(m), wparam, lparam)
	return ret
}

// New creates the hidden window and returns a Tray ready to receive icons.
func New() (*Tray, error) {
	hinst, _, _ := procGetModuleHandle.Call(0)
	if hinst == 0 {
		return nil, fmt.Errorf("GetModuleHandle failed")
	}
	className, err := windows.UTF16PtrFromString("gpu-tray-mon")
	if err != nil {
		return nil, err
	}
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     windows.Handle(hinst),
		lpszClassName: className,
	}
	if r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, fmt.Errorf("RegisterClassEx failed")
	}
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0,
		0, 0, 0, 0,
		0, // parent: hidden top-level window
		0,
		uintptr(hinst),
		0,
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowEx failed")
	}
	t := &Tray{
		hwnd:           windows.Handle(hwnd),
		callback:       wmApp + 1,
		taskbarCreated: registerWindowMessage("TaskbarCreated"),
		icons:          map[uint32]*entry{},
	}
	registerTray(t)
	return t, nil
}

// SetIconEvent sets the handler for tray callbacks (uID, mouse message).
func (t *Tray) SetIconEvent(fn func(uID uint32, event uintptr)) {
	t.onIconEvent = fn
}

// MenuItem is a single entry in the right-click popup menu.
type MenuItem struct {
	ID   int
	Text string
}

// ShowMenu displays a popup menu at the cursor and returns the selected
// item's ID, or 0 if dismissed. It must be called on the window's message
// thread (e.g. from within the icon event handler).
func (t *Tray) ShowMenu(items []MenuItem) int {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return 0
	}
	defer procDestroyMenu.Call(menu)
	for _, it := range items {
		p, err := windows.UTF16PtrFromString(it.Text)
		if err != nil {
			continue
		}
		procAppendMenuW.Call(menu, mfString, uintptr(it.ID), uintptr(unsafe.Pointer(p)))
	}
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(t.hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmNonotify|tpmRightButton, uintptr(pt.x), uintptr(pt.y), 0, uintptr(t.hwnd), 0)
	procPostMessage.Call(uintptr(t.hwnd), wmNull, 0, 0)
	return int(cmd)
}

// AddIcon adds a blank icon identified by uID (and guid for version 4).
func (t *Tray) AddIcon(uID uint32, guid windows.GUID) error {
	blank := image.NewRGBA(image.Rect(0, 0, 1, 1))
	hicon, err := iconFromRGBA(blank)
	if err != nil {
		return err
	}
	nid := notifyIconData{
		cbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		hWnd:             t.hwnd,
		uID:              uID,
		uFlags:           nifMessage | nifIcon | nifTip | nifGuid | nifShowtip,
		uCallbackMessage: t.callback,
		hIcon:            hicon,
		guidItem:         guid,
	}
	setTip(&nid.szTip, "gpu-tray-mon")
	if r, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		procDeleteObject.Call(uintptr(hicon))
		return fmt.Errorf("Shell_NotifyIcon(NIM_ADD) failed")
	}
	ver := notifyIconData{
		cbSize:          uint32(unsafe.Sizeof(notifyIconData{})),
		hWnd:            t.hwnd,
		uID:             uID,
		guidItem:        guid,
		uTimeoutVersion: notifyIconVersion4,
	}
	procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&ver)))

	t.mu.Lock()
	t.icons[uID] = &entry{guid: guid, hicon: hicon}
	t.mu.Unlock()
	return nil
}

// SetIcon replaces icon uID's bitmap and tooltip.
func (t *Tray) SetIcon(uID uint32, img *image.RGBA, tip string) error {
	hicon, err := iconFromRGBA(img)
	if err != nil {
		return err
	}
	t.mu.Lock()
	e := t.icons[uID]
	t.mu.Unlock()
	if e == nil {
		procDeleteObject.Call(uintptr(hicon))
		return fmt.Errorf("no icon %d", uID)
	}

	nid := notifyIconData{
		cbSize:   uint32(unsafe.Sizeof(notifyIconData{})),
		hWnd:     t.hwnd,
		uID:      uID,
		uFlags:   nifIcon | nifTip | nifGuid,
		hIcon:    hicon,
		guidItem: e.guid,
	}
	setTip(&nid.szTip, tip)
	if r, _, _ := procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid))); r == 0 {
		procDeleteObject.Call(uintptr(hicon))
		return fmt.Errorf("Shell_NotifyIcon(NIM_MODIFY) failed")
	}

	t.mu.Lock()
	old := e.hicon
	e.hicon = hicon
	e.tip = tip
	t.mu.Unlock()
	if old != 0 {
		procDeleteObject.Call(uintptr(old))
	}
	return nil
}

// RemoveIcon deletes icon uID and frees its bitmap.
func (t *Tray) RemoveIcon(uID uint32) error {
	t.mu.Lock()
	e := t.icons[uID]
	delete(t.icons, uID)
	t.mu.Unlock()
	if e == nil {
		return nil
	}
	nid := notifyIconData{
		cbSize:   uint32(unsafe.Sizeof(notifyIconData{})),
		hWnd:     t.hwnd,
		uID:      uID,
		uFlags:   nifGuid,
		guidItem: e.guid,
	}
	r, _, _ := procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if e.hicon != 0 {
		procDeleteObject.Call(uintptr(e.hicon))
	}
	if r == 0 {
		return fmt.Errorf("Shell_NotifyIcon(NIM_DELETE) failed")
	}
	return nil
}

// reAddAll re-registers every icon with the shell. Called when Explorer
// restarts (TaskbarCreated) and drops the tray icons.
func (t *Tray) reAddAll() {
	t.mu.Lock()
	type pair struct {
		uID uint32
		e   *entry
	}
	pairs := make([]pair, 0, len(t.icons))
	for uID, e := range t.icons {
		pairs = append(pairs, pair{uID, e})
	}
	t.mu.Unlock()

	for _, p := range pairs {
		nid := notifyIconData{
			cbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
			hWnd:             t.hwnd,
			uID:              p.uID,
			uFlags:           nifMessage | nifIcon | nifTip | nifGuid | nifShowtip,
			uCallbackMessage: t.callback,
			hIcon:            p.e.hicon,
			guidItem:         p.e.guid,
		}
		setTip(&nid.szTip, p.e.tip)
		procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		ver := notifyIconData{
			cbSize:          uint32(unsafe.Sizeof(notifyIconData{})),
			hWnd:            t.hwnd,
			uID:             p.uID,
			guidItem:        p.e.guid,
			uTimeoutVersion: notifyIconVersion4,
		}
		procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&ver)))
	}
}

// Run blocks in the message loop until the window closes (WM_QUIT).
func (t *Tray) Run() {
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// Stop asks the message loop to shut down.
func (t *Tray) Stop() {
	procPostMessage.Call(uintptr(t.hwnd), wmClose, 0, 0)
}

// NewGUID returns a fresh random GUID for a tray icon.
func NewGUID() (windows.GUID, error) {
	var g windows.GUID
	r, _, _ := procCoCreateGuid.Call(uintptr(unsafe.Pointer(&g)))
	if r != 0 {
		return g, fmt.Errorf("CoCreateGuid failed 0x%08x", uint32(r))
	}
	return g, nil
}

// IconSize returns the DPI-scaled system-tray icon size in pixels, so icons
// render crisply regardless of display scale.
func IconSize() (w, h int) {
	cx, _, _ := procGetSystemMetrics.Call(smCXSmallIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCYSmallIcon)
	return int(cx), int(cy)
}

func registerWindowMessage(name string) uint32 {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

// RenderSparkline renders a 0..1 normalized sample series as a filled
// area chart in a single color onto a transparent wxh image.
func RenderSparkline(samples []float64, c color.RGBA, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	n := len(samples)
	if n == 0 {
		return img
	}
	for i, v := range samples {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		bar := int(v * float64(h))
		x := i * w / n
		for y := h - bar; y < h; y++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// RenderMemoryBar renders two stacked battery bars: dedicated on top (solid
// color) and shared below (a lighter variant of the same color), each filled
// left-to-right proportional to its 0..1 fraction.
func RenderMemoryBar(dedicatedFrac, sharedFrac float64, c color.RGBA, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	barH := (h - 2) / 2
	gap := h - 2*barH
	drawBar(img, 0, 0, w, barH, dedicatedFrac, c)
	drawBar(img, 0, barH+gap, w, barH, sharedFrac, lighten(c))
	return img
}

func drawBar(img *image.RGBA, x0, y0, w, h int, frac float64, c color.RGBA) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fill := int(frac * float64(w))
	outline := color.RGBA{R: 0x66, G: 0x66, B: 0x66, A: 0xff}
	for x := x0; x < x0+w; x++ {
		img.SetRGBA(x, y0, outline)
		img.SetRGBA(x, y0+h-1, outline)
	}
	for y := y0; y < y0+h; y++ {
		img.SetRGBA(x0, y, outline)
		img.SetRGBA(x0+w-1, y, outline)
	}
	for y := y0 + 1; y < y0+h-1; y++ {
		for x := x0 + 1; x < x0+1+fill; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func lighten(c color.RGBA) color.RGBA {
	return color.RGBA{
		R: uint8(float64(c.R)*0.55 + 255*0.45),
		G: uint8(float64(c.G)*0.55 + 255*0.45),
		B: uint8(float64(c.B)*0.55 + 255*0.45),
		A: 0xff,
	}
}

// RenderX draws an "X" (two thick diagonals) in the given color onto a
// transparent wxh image. Used for the "no GPU" error icon.
func RenderX(c color.RGBA, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if w <= 1 || h <= 1 {
		return img
	}
	thick := 2
	if w < 16 {
		thick = 1
	}
	for x := 0; x < w; x++ {
		y1 := x * (h - 1) / (w - 1)   // top-left -> bottom-right
		y2 := (h - 1) - x*(h-1)/(w-1) // bottom-left -> top-right
		for dy := 0; dy < thick; dy++ {
			plot(img, x, y1+dy, c)
			plot(img, x, y1-dy, c)
			plot(img, x, y2+dy, c)
			plot(img, x, y2-dy, c)
		}
	}
	return img
}

func plot(img *image.RGBA, x, y int, c color.RGBA) {
	if x >= 0 && x < img.Bounds().Dx() && y >= 0 && y < img.Bounds().Dy() {
		img.SetRGBA(x, y, c)
	}
}

// --- icon creation ---

// iconFromRGBA builds an HICON from an RGBA image via a 32-bpp DIB section
// and CreateIconIndirect (alpha preserved, transparent background).
func iconFromRGBA(img *image.RGBA) (windows.Handle, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return 0, fmt.Errorf("empty image")
	}

	bih := bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       int32(w),
		biHeight:      -int32(h), // top-down
		biPlanes:      1,
		biBitCount:    32,
		biCompression: 0, // BI_RGB
	}

	dc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, dc)

	var bits *byte
	hbm, _, _ := procCreateDIBSection.Call(
		dc,
		uintptr(unsafe.Pointer(&bih)),
		0, // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if hbm == 0 {
		return 0, fmt.Errorf("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(hbm)

	if bits != nil {
		dst := unsafe.Slice(bits, w*h*4)
		pix := img.Pix
		for i := 0; i < w*h; i++ {
			// DIB 32bpp is BGRA in memory.
			dst[i*4+0] = pix[i*4+2] // B
			dst[i*4+1] = pix[i*4+1] // G
			dst[i*4+2] = pix[i*4+0] // R
			dst[i*4+3] = pix[i*4+3] // A
		}
	}

	// Monochrome AND mask, all-zero (fully opaque: alpha channel governs).
	maskStride := (w + 15) / 16 * 2
	maskBits := make([]byte, maskStride*h)
	hbmMask, _, _ := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	if hbmMask == 0 {
		return 0, fmt.Errorf("CreateBitmap (mask) failed")
	}
	defer procDeleteObject.Call(hbmMask)

	ii := iconInfo{
		fIcon:    1,
		hbmColor: windows.Handle(hbm),
		hbmMask:  windows.Handle(hbmMask),
	}
	hicon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	if hicon == 0 {
		return 0, fmt.Errorf("CreateIconIndirect failed")
	}
	return windows.Handle(hicon), nil
}

func setTip(dst *[128]uint16, s string) {
	for i := range dst {
		dst[i] = 0
	}
	enc, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(enc) > len(dst) {
		enc = enc[:len(dst)]
	}
	copy(dst[:], enc)
}
