package plan

import (
	"strconv"
	"strings"
)

// A vendor that ships one pack several ways usually says so in the folder
// name itself: "Thump 16 bit mono" under "Thump", "Pack 24 bit stereo",
// "808 Kit 16-Bit WAV". The words after the pack's own name are pure
// format vocabulary — bit depth, channel count, container — and carry no
// musical information. heuristicTreeRank recognizes that shape with no
// annotation at all, so a vendor nobody has written up still collapses to
// one render per sample instead of erroring three sources onto one path.
//
// The rule is deliberately narrow: after peeling the pack's name off the
// front, every remaining word must be format vocabulary, and at least one
// must anchor the reading — a channel word or a bit depth. A dir named
// "Kicks mono" keeps its kicks; a bare "WAV" or "48k" is not claimed
// (that shape belongs to annotated vendors, where a human has looked).
// An explicitly annotated dir never reaches this function — the vendor's
// own [[dir]] map wins first (see treeStripper.strip).

// treeWords is the format vocabulary: words that may appear in a format
// tree's name beyond the pack's own. Numbers are handled separately.
var treeWords = map[string]bool{
	"pack": true, "wav": true, "wavs": true, "aiff": true,
	"bit": true, "bits": true, "mono": true, "stereo": true,
	"khz": true, "hz": true, "k": true,
}

// treeAnchors are the words that make the reading safe on their own:
// nothing but a format tree calls itself mono, stereo, or N-bit.
var treeAnchors = map[string]bool{
	"bit": true, "bits": true, "mono": true, "stereo": true,
}

// treeTokens lowercases and splits a dir name on the separators vendors
// actually use, so "16-Bit_Stereo" and "16 bit stereo" read the same.
func treeTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '.'
	})
}

// treeNumber reads a numeric format token: "16", "24", "44100", and the
// glued forms "16bit" / "48khz". Returns the leading number and whether
// the token is format vocabulary at all.
func treeNumber(tok string) (n int, ok bool) {
	digits := tok
	for i, r := range tok {
		if r < '0' || r > '9' {
			digits, tok = tok[:i], tok[i:]
			break
		}
		if i == len(tok)-1 {
			tok = ""
		}
	}
	if digits == "" {
		return 0, false
	}
	if tok != "" && !treeWords[tok] {
		return 0, false
	}
	n, _ = strconv.Atoi(digits)
	return n, true
}

// heuristicTreeRank reports whether dir (directly under a pack dir named
// packDir) reads as a format tree by naming alone, and ranks it by the
// fidelity its name declares — lower is closer to a master render, so the
// vendor-style tiebreak in betterCut keeps working (quality still leads;
// the rank only breaks ties).
func heuristicTreeRank(packDir, dir string) (rank int, ok bool) {
	pt, dt := treeTokens(packDir), treeTokens(dir)
	for len(pt) > 0 && len(dt) > 0 && pt[0] == dt[0] {
		pt, dt = pt[1:], dt[1:]
	}
	if len(dt) == 0 {
		return 0, false // the pack's own name, or nothing left to read
	}
	bits, channels, anchored := 16, 2, false
	for i, tok := range dt {
		if treeWords[tok] {
			if treeAnchors[tok] {
				anchored = true
			}
			if tok == "mono" {
				channels = 1
			}
			continue
		}
		n, num := treeNumber(tok)
		if !num {
			return 0, false // a word that carries meaning — not a tree
		}
		// a bare depth number counts as its own anchor when the next
		// word spells "bit" ("16 bit mono"); glued forms ("16bit") too
		if strings.Contains(tok, "bit") || (i+1 < len(dt) && treeAnchors[dt[i+1]]) {
			anchored = true
		}
		if n == 8 || n == 16 || n == 24 || n == 32 {
			bits = n
		}
	}
	if !anchored {
		return 0, false
	}
	return (32-bits)*2 + (2 - channels), true
}
