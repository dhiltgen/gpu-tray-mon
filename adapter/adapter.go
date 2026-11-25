//go:build windows

// Package adapter enumerates physical display GPU adapters via D3DKMT and
// reads each adapter's name and dedicated-VRAM total through
// D3DKMTQueryAdapterInfo. Non-display compute adapters (e.g. an NPU) and
// software/basic render adapters (zero dedicated VRAM) are excluded.
package adapter

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Info describes one display GPU adapter.
type Info struct {
	LUID                string // canonical "luid_0xHI_0xLO" (matches pdh.LUID)
	StableID            string // stable across reboots (PCI bus:device:function)
	Name                string
	DedicatedTotalBytes int64 // total VRAM on the card, for fill-percentage math
	SharedTotalBytes    int64 // system memory available to the GPU
}

var (
	libgdi32             = windows.NewLazySystemDLL("gdi32.dll")
	procEnumAdapters2    = libgdi32.NewProc("D3DKMTEnumAdapters2")
	procQueryAdapterInfo = libgdi32.NewProc("D3DKMTQueryAdapterInfo")
)

// KMTQUERYADAPTERINFOTYPE values (d3dkmthk.h).
const (
	kmtQueryTypeSegmentSize    = 3 // KMTQAITYPE_GETSEGMENTSIZE
	kmtQueryTypeAdapterAddress = 6 // KMTQAITYPE_ADAPTERADDRESS
	kmtQueryTypeRegistryInfo   = 8 // KMTQAITYPE_ADAPTERREGISTRYINFO
)

type luid struct {
	LowPart  uint32
	HighPart int32
}

// d3dkmtAdapterInfo mirrors D3DKMT_ADAPTERINFO.
type d3dkmtAdapterInfo struct {
	hAdapter                     uint32
	adapterLuid                  luid
	numOfSources                 uint32
	bPresentMoveRegionsPreferred uint32
}

// d3dkmtEnumAdapters2 mirrors D3DKMT_ENUMADAPTERS2: NumAdapters + a pointer
// to a separately-allocated D3DKMT_ADAPTERINFO array.
type d3dkmtEnumAdapters2 struct {
	numAdapters uint32
	_           uint32
	pAdapters   *d3dkmtAdapterInfo
}

// d3dkmtQueryAdapterInfo mirrors D3DKMT_QUERYADAPTERINFO.
type d3dkmtQueryAdapterInfo struct {
	hAdapter              uint32
	queryType             int32
	pPrivateDriverData    unsafe.Pointer
	privateDriverDataSize uint32
}

// d3dkmtSegmentSizeInfo mirrors D3DKMT_SEGMENTSIZEINFO.
type d3dkmtSegmentSizeInfo struct {
	DedicatedVideoMemorySize  uint64
	DedicatedSystemMemorySize uint64
	SharedSystemMemorySize    uint64
}

// d3dkmtAdapterRegistryInfo mirrors D3DKMT_ADAPTERREGISTRYINFO.
type d3dkmtAdapterRegistryInfo struct {
	AdapterString [260]uint16 // MAX_PATH
	BiosString    [260]uint16
	DacType       [260]uint16
	ChipType      [260]uint16
}

// d3dkmtAdapterAddress mirrors D3DKMT_ADAPTERADDRESS (the PCI BDF).
type d3dkmtAdapterAddress struct {
	BusNumber      uint32
	DeviceNumber   uint32
	FunctionNumber uint32
}

// Enumerate returns the chartable GPU adapters (dedicated VRAM > 0).
func Enumerate() ([]Info, error) {
	details, err := EnumerateDetailed()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(details))
	for _, d := range details {
		if d.Chartable {
			out = append(out, d.Info)
		}
	}
	return out, nil
}

// Detail is one D3DKMT display adapter plus whether it is chartable and, if
// not, why it was skipped. Used for -dump diagnostics.
type Detail struct {
	Info
	Chartable bool
	Reason    string
}

// EnumerateDetailed returns every D3DKMT display adapter with a chartable
// flag and, for skipped adapters, the reason.
func EnumerateDetailed() ([]Detail, error) {
	ads, err := enumAdapters()
	if err != nil {
		return nil, err
	}
	out := make([]Detail, 0, len(ads))
	for _, a := range ads {
		info := Info{LUID: luidString(a.adapterLuid), Name: queryName(a.hAdapter)}
		info.StableID = luidString(a.adapterLuid) // fallback if the BDF query fails
		if addr, err := queryAdapterAddress(a.hAdapter); err == nil {
			info.StableID = fmt.Sprintf("%x:%x:%x", addr.BusNumber, addr.DeviceNumber, addr.FunctionNumber)
		}
		seg, err := querySegmentSize(a.hAdapter)
		switch {
		case err != nil:
			out = append(out, Detail{Info: info, Reason: fmt.Sprintf("memory query failed: %v", err)})
		case seg.DedicatedVideoMemorySize == 0:
			out = append(out, Detail{Info: info, Reason: "no dedicated VRAM (software render or non-display adapter)"})
		default:
			info.DedicatedTotalBytes = int64(seg.DedicatedVideoMemorySize)
			info.SharedTotalBytes = int64(seg.SharedSystemMemorySize)
			out = append(out, Detail{Info: info, Chartable: true})
		}
	}
	return out, nil
}

func enumAdapters() ([]d3dkmtAdapterInfo, error) {
	// First call with pAdapters=nil: driver reports the max adapter count.
	desc := d3dkmtEnumAdapters2{}
	r, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&desc)))
	if n := int(desc.numAdapters); n == 0 {
		return nil, fmt.Errorf("D3DKMTEnumAdapters2: no adapters (status 0x%08x)", uint32(r))
	}

	arr := make([]d3dkmtAdapterInfo, desc.numAdapters)
	desc2 := d3dkmtEnumAdapters2{numAdapters: desc.numAdapters, pAdapters: &arr[0]}
	r, _, _ = procEnumAdapters2.Call(uintptr(unsafe.Pointer(&desc2)))
	if uint32(r) != 0 {
		return nil, fmt.Errorf("D3DKMTEnumAdapters2: status 0x%08x", uint32(r))
	}
	return arr[:desc2.numAdapters], nil
}

func querySegmentSize(hAdapter uint32) (d3dkmtSegmentSizeInfo, error) {
	var out d3dkmtSegmentSizeInfo
	q := d3dkmtQueryAdapterInfo{
		hAdapter:              hAdapter,
		queryType:             kmtQueryTypeSegmentSize,
		pPrivateDriverData:    unsafe.Pointer(&out),
		privateDriverDataSize: uint32(unsafe.Sizeof(out)),
	}
	r, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&q)))
	if uint32(r) != 0 {
		return out, fmt.Errorf("D3DKMTQueryAdapterInfo(segmentsize): status 0x%08x", uint32(r))
	}
	return out, nil
}

func queryAdapterAddress(hAdapter uint32) (d3dkmtAdapterAddress, error) {
	var out d3dkmtAdapterAddress
	q := d3dkmtQueryAdapterInfo{
		hAdapter:              hAdapter,
		queryType:             kmtQueryTypeAdapterAddress,
		pPrivateDriverData:    unsafe.Pointer(&out),
		privateDriverDataSize: uint32(unsafe.Sizeof(out)),
	}
	r, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&q)))
	if uint32(r) != 0 {
		return out, fmt.Errorf("D3DKMTQueryAdapterInfo(adapteraddress): status 0x%08x", uint32(r))
	}
	return out, nil
}

func queryName(hAdapter uint32) string {
	var out d3dkmtAdapterRegistryInfo
	q := d3dkmtQueryAdapterInfo{
		hAdapter:              hAdapter,
		queryType:             kmtQueryTypeRegistryInfo,
		pPrivateDriverData:    unsafe.Pointer(&out),
		privateDriverDataSize: uint32(unsafe.Sizeof(out)),
	}
	r, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&q)))
	if uint32(r) != 0 {
		return ""
	}
	return windows.UTF16ToString(out.AdapterString[:])
}

func luidString(l luid) string {
	return fmt.Sprintf("luid_0x%08x_0x%08x", uint32(l.HighPart), l.LowPart)
}
