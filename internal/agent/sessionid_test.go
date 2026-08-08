package agent

import (
	"strings"
	"testing"
)

func TestLooksLikeUUID_Accepts(t *testing.T) {
	for _, id := range []string{
		"0198f1b2-4c3d-7a1e-8b2f-000000000abc",
		"0198F1B2-4C3D-7A1E-8B2F-000000000ABC", // upper case
		"0198f1b2-4C3D-7a1e-8B2F-000000000abc", // mixed
		"00000000-0000-0000-0000-000000000000", // nil UUID
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		// Version and variant nibbles are deliberately not checked, so a
		// version this code has never heard of still passes. Refusing a real
		// id is the expensive direction — see the doc comment.
		"0198f1b2-4c3d-9a1e-1b2f-000000000abc",
	} {
		if !LooksLikeUUID(id) {
			t.Errorf("LooksLikeUUID(%q) = false, want true", id)
		}
	}
}

// TestLooksLikeUUID_RejectsNonCanonicalSpellings is what the length test buys,
// and it is the only reason this is not a bare call to uuid.Validate: that
// function accepts all three of these. They are UUIDs, but no agent reports one
// this way and none would take it back on a command line, so recording one
// would leave a session pointing at an id its own agent cannot resolve.
func TestLooksLikeUUID_RejectsNonCanonicalSpellings(t *testing.T) {
	for _, id := range []string{
		"0198f1b24c3d7a1e8b2f000000000abc",              // 32 bare hex digits
		"{0198f1b2-4c3d-7a1e-8b2f-000000000abc}",        // braced
		"urn:uuid:0198f1b2-4c3d-7a1e-8b2f-000000000abc", // URN
	} {
		if LooksLikeUUID(id) {
			t.Errorf("LooksLikeUUID(%q) = true; only the canonical spelling is an id jind-ai can use", id)
		}
	}
}

func TestLooksLikeUUID_Rejects(t *testing.T) {
	for _, id := range []string{
		"",
		"abc",
		"ses_084426f78ffeXBrPh5ABEu2dNX",         // opencode's shape
		"0198f1b2-4c3d-7a1e-8b2f-000000000ab",    // last group short
		"0198f1b2-4c3d-7a1e-8b2f-000000000abcd",  // last group long
		"0198f1b-24c3d-7a1e-8b2f-000000000abc",   // hyphen misplaced
		"0198f1b2-4c3d-7a1e-8b2f-000000000abg",   // g is not hex
		"0198f1b2_4c3d_7a1e_8b2f_000000000abc",   // underscores, not hyphens
		" 0198f1b2-4c3d-7a1e-8b2f-000000000abc",  // leading space
		"0198f1b2-4c3d-7a1e-8b2f-000000000abc ",  // trailing space
		"0198f1b2-4c3d-7a1e-8b2f-000000000abc\n", // trailing newline
	} {
		if LooksLikeUUID(id) {
			t.Errorf("LooksLikeUUID(%q) = true, want false", id)
		}
	}
}

// TestLooksLikeUUID_RejectsAnEmbeddedUUID guards the direction that matters for
// injection: a value that merely CONTAINS something valid is not valid.
// Accepting one would let `$(...)` ride alongside a real id.
func TestLooksLikeUUID_RejectsAnEmbeddedUUID(t *testing.T) {
	valid := "0198f1b2-4c3d-7a1e-8b2f-000000000abc"
	for _, affix := range []string{"$(touch x)", "x;", "`id`", "'"} {
		if LooksLikeUUID(affix + valid) {
			t.Errorf("LooksLikeUUID(%q) = true, want false", affix+valid)
		}
		if LooksLikeUUID(valid + affix) {
			t.Errorf("LooksLikeUUID(%q) = true, want false", valid+affix)
		}
	}
}

// TestLooksLikeUUID_RejectsNonASCII pins that a multi-byte rune cannot pass as a
// hex digit. len() counts bytes, so a 36-byte string of non-ASCII reaches the
// parse rather than being cut by the length test — this is the case that would
// break if the length were ever measured in runes.
func TestLooksLikeUUID_RejectsNonASCII(t *testing.T) {
	// Fullwidth digit: reads as a UUID to a human, and is not hex.
	if id := "０198f1b2-4c3d-7a1e-8b2f-000000000ab"; LooksLikeUUID(id) {
		t.Errorf("LooksLikeUUID(%q) = true, want false", id)
	}
	if id := strings.Repeat("a", 12) + strings.Repeat("あ", 8); LooksLikeUUID(id) {
		t.Errorf("LooksLikeUUID(%q) = true, want false", id)
	}
}
