package agent

import (
	"testing"
)

func TestIsCanonicalThinkingLevel(t *testing.T) {
	for _, l := range []ThinkingLevel{
		ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium,
		ThinkingHigh, ThinkingXHigh, ThinkingMax,
	} {
		if !IsCanonicalThinkingLevel(l) {
			t.Errorf("IsCanonicalThinkingLevel(%q) = false, want true", l)
		}
	}
	for _, l := range []ThinkingLevel{"", "bogus", "OFF", "Max"} {
		if IsCanonicalThinkingLevel(l) {
			t.Errorf("IsCanonicalThinkingLevel(%q) = true, want false", l)
		}
	}
}

func TestClampThinkingLevel_Empty(t *testing.T) {
	got := ClampThinkingLevel("", []ThinkingLevel{ThinkingOff, ThinkingHigh})
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestClampThinkingLevel_EmptyAvailable(t *testing.T) {
	got := ClampThinkingLevel(ThinkingHigh, nil)
	if got != ThinkingOff {
		t.Errorf("got %q, want off", got)
	}
}

func TestClampThinkingLevel_PassThrough(t *testing.T) {
	avail := []ThinkingLevel{ThinkingOff, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax}
	for _, l := range avail {
		got := ClampThinkingLevel(l, avail)
		if got != l {
			t.Errorf("ClampThinkingLevel(%q) = %q, want %q", l, got, l)
		}
	}
}

func TestClampThinkingLevel_WalksDownLadder(t *testing.T) {
	cases := []struct {
		name      string
		requested ThinkingLevel
		available []ThinkingLevel
		want      ThinkingLevel
	}{
		{
			name:      "max → high when xhigh+max unsupported",
			requested: ThinkingMax,
			available: []ThinkingLevel{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh},
			want:      ThinkingHigh,
		},
		{
			name:      "max → xhigh when only max unsupported",
			requested: ThinkingMax,
			available: []ThinkingLevel{ThinkingOff, ThinkingHigh, ThinkingXHigh},
			want:      ThinkingXHigh,
		},
		{
			name:      "xhigh → high when xhigh unsupported",
			requested: ThinkingXHigh,
			available: []ThinkingLevel{ThinkingOff, ThinkingLow, ThinkingMedium, ThinkingHigh},
			want:      ThinkingHigh,
		},
		{
			name:      "high → off on non-reasoning model",
			requested: ThinkingHigh,
			available: []ThinkingLevel{ThinkingOff},
			want:      ThinkingOff,
		},
		{
			name:      "medium → low when medium missing",
			requested: ThinkingMedium,
			available: []ThinkingLevel{ThinkingOff, ThinkingLow},
			want:      ThinkingLow,
		},
		{
			name:      "minimal → off when minimal missing",
			requested: ThinkingMinimal,
			available: []ThinkingLevel{ThinkingOff},
			want:      ThinkingOff,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClampThinkingLevel(tc.requested, tc.available)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClampThinkingLevel_UnknownLevel(t *testing.T) {
	avail := []ThinkingLevel{ThinkingOff, ThinkingHigh}
	got := ClampThinkingLevel(ThinkingLevel("bogus"), avail)
	if got != ThinkingOff {
		t.Errorf("got %q, want off", got)
	}
	// Unknown but listed in avail: pass through.
	avail2 := []ThinkingLevel{ThinkingOff, ThinkingLevel("bogus")}
	got2 := ClampThinkingLevel(ThinkingLevel("bogus"), avail2)
	if got2 != ThinkingLevel("bogus") {
		t.Errorf("got %q, want bogus", got2)
	}
}
