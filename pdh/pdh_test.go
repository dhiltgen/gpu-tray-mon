//go:build windows

package pdh

import "testing"

func TestLuidOf(t *testing.T) {
	in := "pid_22624_luid_0x00000000_0x0001BD79_phys_0_eng_8_engtype_VideoProcessing"
	if got := luidOf(in); got != "luid_0x00000000_0x0001BD79" {
		t.Fatalf("luidOf(%q) = %q", in, got)
	}
	if got := luidOf("no luid here"); got != "" {
		t.Fatalf("luidOf(no luid) = %q", got)
	}
}

func TestEngineTypeOf(t *testing.T) {
	if got := engineTypeOf("pid_1_luid_0x0_0x0_phys_0_eng_0_engtype_VideoProcessing"); got != "VideoProcessing" {
		t.Fatalf("engineTypeOf = %q", got)
	}
	if got := engineTypeOf("pid_1_engtype_"); got != "" {
		t.Fatalf("engineTypeOf(empty) = %q", got)
	}
	if got := engineTypeOf("no marker"); got != "" {
		t.Fatalf("engineTypeOf(no marker) = %q", got)
	}
}

func TestAggregateUtilization(t *testing.T) {
	// Adapter A: two 3D engines (30+40) and one Copy (50) -> max across
	// types = 70. Adapter B: one 3D at 20 -> 20.
	samples := []engineSample{
		{name: "pid_1_luid_0x00000000_0x0000000A_phys_0_eng_0_engtype_3D", value: 30},
		{name: "pid_2_luid_0x00000000_0x0000000A_phys_0_eng_1_engtype_3D", value: 40},
		{name: "pid_3_luid_0x00000000_0x0000000A_phys_0_eng_2_engtype_Copy", value: 50},
		{name: "pid_4_luid_0x00000000_0x0000000B_phys_0_eng_0_engtype_3D", value: 20},
		{name: "ignored_no_luid", value: 99},
	}
	got := aggregateUtilization(samples)
	if got["luid_0x00000000_0x0000000A"] != 70 {
		t.Errorf("adapter A = %v, want 70", got["luid_0x00000000_0x0000000A"])
	}
	if got["luid_0x00000000_0x0000000B"] != 20 {
		t.Errorf("adapter B = %v, want 20", got["luid_0x00000000_0x0000000B"])
	}
	if len(got) != 2 {
		t.Errorf("expected 2 adapters, got %v", got)
	}
}
