//go:build windows

// Package pdh reads GPU utilization and memory via the Windows PDH
// performance counter API. It is vendor-agnostic (NVIDIA, AMD, Intel).
package pdh

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	formatDouble uint32 = 0x00000200 // PDH_FMT_DOUBLE
	formatLarge  uint32 = 0x00000400 // PDH_FMT_LARGE

	errMoreData uint32 = 0x800007D2 // PDH_MORE_DATA
)

var libpdh = windows.NewLazySystemDLL("pdh.dll")

var (
	procOpenQuery         = libpdh.NewProc("PdhOpenQueryW")
	procAddEnglishCounter = libpdh.NewProc("PdhAddEnglishCounterW")
	procCollectQueryData  = libpdh.NewProc("PdhCollectQueryData")
	procGetFormattedArray = libpdh.NewProc("PdhGetFormattedCounterArrayW")
	procCloseQuery        = libpdh.NewProc("PdhCloseQuery")
)

// Error is a PDH error code.
type Error uint32

func (e Error) Error() string { return fmt.Sprintf("pdh error 0x%08x", uint32(e)) }

// pdhFmtCounterValue mirrors PDH_FMT_COUNTERVALUE (CStatus + 8-byte union).
type pdhFmtCounterValue struct {
	cStatus uint32
	_       uint32
	value   uint64
}

func (v pdhFmtCounterValue) asDouble() float64 { return math.Float64frombits(v.value) }
func (v pdhFmtCounterValue) asLarge() int64    { return int64(v.value) }

// pdhFmtCounterValueItemW mirrors PDH_FMT_COUNTERVALUE_ITEM_W.
type pdhFmtCounterValueItemW struct {
	szName   *uint16
	fmtValue pdhFmtCounterValue
}

// AdapterStats is one GPU adapter's utilization and memory for a sample.
type AdapterStats struct {
	LUID           string
	Utilization    float64 // percent, Task Manager semantics
	DedicatedBytes int64   // VRAM physically on the card, in use
	SharedBytes    int64   // system memory committed for the GPU
}

const (
	pathUtil   = `\GPU Engine(*)\Utilization Percentage`
	pathDed    = `\GPU Adapter Memory(*)\Dedicated Usage`
	pathShared = `\GPU Adapter Memory(*)\Shared Usage`
)

// Collector reads the three wildcard counters and aggregates them per adapter.
type Collector struct {
	mu          sync.Mutex
	query       uintptr
	counterUtil uintptr
	counterDed  uintptr
	counterShr  uintptr
	closed      bool
}

// NewCollector opens a PDH query, adds the GPU counters, and primes the
// query (first collect establishes the baseline for the rate counter).
func NewCollector() (*Collector, error) {
	q, err := openQuery()
	if err != nil {
		return nil, err
	}
	c := &Collector{query: q}

	fail := func(err error) (*Collector, error) {
		closeQuery(q)
		return nil, err
	}

	if c.counterUtil, err = addEnglishCounter(q, pathUtil); err != nil {
		return fail(fmt.Errorf("add %s: %w", pathUtil, err))
	}
	if c.counterDed, err = addEnglishCounter(q, pathDed); err != nil {
		return fail(fmt.Errorf("add %s: %w", pathDed, err))
	}
	if c.counterShr, err = addEnglishCounter(q, pathShared); err != nil {
		return fail(fmt.Errorf("add %s: %w", pathShared, err))
	}
	if err := collectQueryData(q); err != nil {
		return fail(err)
	}
	return c, nil
}

// Close releases the PDH query. Safe to call more than once.
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return closeQuery(c.query)
}

// Sample collects one fresh reading and returns per-adapter stats.
// The first call after NewCollector already has a valid utilization value.
func (c *Collector) Sample() ([]AdapterStats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("collector closed")
	}
	if err := collectQueryData(c.query); err != nil {
		return nil, err
	}

	utilItems, err := getFormattedArray(c.counterUtil, formatDouble)
	if err != nil {
		return nil, fmt.Errorf("utilization: %w", err)
	}
	dedItems, err := getFormattedArray(c.counterDed, formatLarge)
	if err != nil {
		return nil, fmt.Errorf("dedicated: %w", err)
	}
	shrItems, err := getFormattedArray(c.counterShr, formatLarge)
	if err != nil {
		return nil, fmt.Errorf("shared: %w", err)
	}

	// Utilization: sum within each engine type, then take the max across
	// types (Task Manager's "GPU %" semantics).
	samples := make([]engineSample, 0, len(utilItems))
	for _, it := range utilItems {
		samples = append(samples, engineSample{name: it.name, value: it.val.asDouble()})
	}

	stats := map[string]*AdapterStats{}
	get := func(luid string) *AdapterStats {
		s := stats[luid]
		if s == nil {
			s = &AdapterStats{LUID: luid}
			stats[luid] = s
		}
		return s
	}
	for luid, util := range aggregateUtilization(samples) {
		get(luid).Utilization = util
	}
	for _, it := range dedItems {
		if luid := luidOf(it.name); luid != "" {
			get(luid).DedicatedBytes = it.val.asLarge()
		}
	}
	for _, it := range shrItems {
		if luid := luidOf(it.name); luid != "" {
			get(luid).SharedBytes = it.val.asLarge()
		}
	}

	out := make([]AdapterStats, 0, len(stats))
	for _, s := range stats {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LUID < out[j].LUID })
	return out, nil
}

// --- low-level PDH calls ---

func openQuery() (uintptr, error) {
	var q uintptr
	r, _, _ := procOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q)))
	if r != 0 {
		return 0, Error(r)
	}
	return q, nil
}

func addEnglishCounter(q uintptr, path string) (uintptr, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var c uintptr
	r, _, _ := procAddEnglishCounter.Call(q, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&c)))
	if r != 0 {
		return 0, Error(r)
	}
	return c, nil
}

func collectQueryData(q uintptr) error {
	r, _, _ := procCollectQueryData.Call(q)
	if r != 0 {
		return Error(r)
	}
	return nil
}

func closeQuery(q uintptr) error {
	r, _, _ := procCloseQuery.Call(q)
	if r != 0 {
		return Error(r)
	}
	return nil
}

type item struct {
	name string
	val  pdhFmtCounterValue
}

// getFormattedArray fetches all instances of a wildcard counter, formatted
// in the requested format. Instance names are decoded into Go strings.
func getFormattedArray(c uintptr, format uint32) ([]item, error) {
	var bufSize, count uint32
	r, _, _ := procGetFormattedArray.Call(
		uintptr(c), uintptr(format),
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != errMoreData {
		return nil, Error(uint32(r))
	}
	buf := make([]byte, bufSize)
	r, _, _ = procGetFormattedArray.Call(
		uintptr(c), uintptr(format),
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&count)),
		uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 {
		return nil, Error(r)
	}
	raw := unsafe.Slice((*pdhFmtCounterValueItemW)(unsafe.Pointer(&buf[0])), int(count))
	out := make([]item, 0, int(count))
	for i := range raw {
		name := ""
		if raw[i].szName != nil {
			name = windows.UTF16PtrToString(raw[i].szName)
		}
		out = append(out, item{name: name, val: raw[i].fmtValue})
	}
	return out, nil
}

// --- instance-name parsing ---

var luidRe = regexp.MustCompile(`luid_0x[0-9a-fA-F]{8}_0x[0-9a-fA-F]{8}`)

// luidOf extracts the "luid_0xHI_0xLO" token from an instance name, or "".
func luidOf(name string) string { return luidRe.FindString(name) }

// engineTypeOf returns the engine type ("3D", "Compute", "Copy", ...) for a
// GPU Engine instance name, or "" if absent/empty.
func engineTypeOf(name string) string {
	const marker = "engtype_"
	i := strings.Index(name, marker)
	if i < 0 {
		return ""
	}
	return name[i+len(marker):]
}

// engineSample is one per-engine utilization reading.
type engineSample struct {
	name  string
	value float64
}

// aggregateUtilization computes per-adapter utilization using Task Manager
// semantics: sum within each engine type, then take the max across types.
func aggregateUtilization(samples []engineSample) map[string]float64 {
	typeEng := map[string]map[string]float64{}
	for _, s := range samples {
		luid := luidOf(s.name)
		if luid == "" {
			continue
		}
		m := typeEng[luid]
		if m == nil {
			m = map[string]float64{}
			typeEng[luid] = m
		}
		m[engineTypeOf(s.name)] += s.value
	}
	out := make(map[string]float64, len(typeEng))
	for luid, eng := range typeEng {
		var max float64
		for _, v := range eng {
			if v > max {
				max = v
			}
		}
		out[luid] = max
	}
	return out
}
