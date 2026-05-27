package agent

// CanonicalThinkingLadder lists thinking levels from highest to lowest.
// ClampThinkingLevel walks down this ladder when the requested level is not
// available for a given model.
var CanonicalThinkingLadder = []ThinkingLevel{
	ThinkingMax,
	ThinkingXHigh,
	ThinkingHigh,
	ThinkingMedium,
	ThinkingLow,
	ThinkingMinimal,
	ThinkingOff,
}

// ClampThinkingLevel reports whether l is one of the recognised
// thinking levels (off, minimal, low, medium, high, xhigh, max).
func IsCanonicalThinkingLevel(l ThinkingLevel) bool {
	for _, c := range CanonicalThinkingLadder {
		if c == l {
			return true
		}
	}
	return false
}

// ClampThinkingLevel returns the highest level in `available` that is at or
// below `requested` on the canonical ladder (max→xhigh→high→medium→low→minimal→off).
//
// If `requested` is empty, it returns "" unchanged (callers treat this as
// "no opinion, leave the current level alone").
//
// If `available` is empty, it falls back to ThinkingOff.
//
// If `requested` is not on the canonical ladder, it is returned unchanged
// when present in `available`, otherwise ThinkingOff is returned.
func ClampThinkingLevel(requested ThinkingLevel, available []ThinkingLevel) ThinkingLevel {
	if requested == "" {
		return ""
	}
	if len(available) == 0 {
		return ThinkingOff
	}
	availSet := make(map[ThinkingLevel]struct{}, len(available))
	for _, l := range available {
		availSet[l] = struct{}{}
	}
	// Find the requested level's position in the canonical ladder.
	startIdx := -1
	for i, l := range CanonicalThinkingLadder {
		if l == requested {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		// Unknown level: accept verbatim if available, else off.
		if _, ok := availSet[requested]; ok {
			return requested
		}
		return ThinkingOff
	}
	// Walk down the ladder from the requested level to off.
	for i := startIdx; i < len(CanonicalThinkingLadder); i++ {
		if _, ok := availSet[CanonicalThinkingLadder[i]]; ok {
			return CanonicalThinkingLadder[i]
		}
	}
	return ThinkingOff
}
