package ableton

import (
	"strings"
	"testing"
)

const live11 = `<?xml version="1.0" encoding="UTF-8"?>
<Ableton MajorVersion="5" MinorVersion="11.0_433" Creator="Ableton Live 11.3.4">
	<GroupDevicePreset>
		<Device>
			<DrumGroupDevice>
				<Branches>
					<DrumBranchPreset Id="0">
						<SampleRef>
							<FileRef>
								<RelativePathType Value="3" />
								<RelativePath Value="Kicks/Kick 01.wav" />
								<Path Value="C:/Users/joe/Splice/Sounds/Big Pack/Kicks/Kick 01.wav" />
								<Type Value="2" />
								<LivePackName Value="" />
								<LivePackId Value="" />
								<OriginalFileSize Value="123456" />
								<OriginalCrc Value="7890" />
							</FileRef>
						</SampleRef>
					</DrumBranchPreset>
					<DrumBranchPreset Id="1">
						<SampleRef>
							<FileRef>
								<RelativePathType Value="3" />
								<RelativePath Value="Snares/Snare &amp; Clap.aif" />
								<Path Value="C:/Users/joe/Splice/Sounds/Big Pack/Snares/Snare &amp; Clap.aif" />
								<Type Value="2" />
								<LivePackName Value="" />
								<LivePackId Value="" />
							</FileRef>
						</SampleRef>
					</DrumBranchPreset>
					<DrumBranchPreset Id="2">
						<SampleRef>
							<FileRef>
								<RelativePathType Value="3" />
								<RelativePath Value="Hats/Missing.wav" />
								<Path Value="C:/Users/joe/Splice/Sounds/Big Pack/Hats/Missing.wav" />
								<Type Value="2" />
							</FileRef>
						</SampleRef>
					</DrumBranchPreset>
				</Branches>
			</DrumGroupDevice>
		</Device>
	</GroupDevicePreset>
</Ableton>
`

const live10 = `<?xml version="1.0" encoding="UTF-8"?>
<Ableton MajorVersion="5" MinorVersion="10.0_377" Creator="Ableton Live 10.1.30">
	<FileRef>
		<HasRelativePath Value="true" />
		<RelativePathType Value="3" />
		<RelativePath>
			<RelativePathElement Id="0" Dir="Kicks" />
		</RelativePath>
		<Name Value="Kick 01.wav" />
		<Type Value="1" />
		<Data>
			00000000
		</Data>
		<RefersToFolder Value="false" />
		<SearchHint>
			<PathHint>
				<RelativePathElement Id="0" Dir="Users" />
				<RelativePathElement Id="1" Dir="joe" />
			</PathHint>
			<FileSize Value="0" />
			<Crc Value="0" />
			<MaxCrcSize Value="0" />
			<HasExtendedInfo Value="false" />
		</SearchHint>
		<LivePackName Value="" />
		<LivePackId Value="" />
	</FileRef>
</Ableton>
`

func TestRoundTrip(t *testing.T) {
	gz := Encode([]byte(live11))
	back, err := Decode(gz)
	if err != nil || string(back) != live11 {
		t.Fatalf("round trip: %v", err)
	}
	if string(Encode([]byte(live11))) != string(gz) {
		t.Fatal("Encode is not deterministic")
	}
}

func TestRefsLive11(t *testing.T) {
	refs := Refs([]byte(live11))
	if len(refs) != 3 {
		t.Fatalf("refs = %d", len(refs))
	}
	if refs[1].Name != "Snare & Clap.aif" || refs[1].Rel != "Snares/Snare & Clap.aif" || refs[1].Type != "3" {
		t.Fatalf("ref[1] = %+v", refs[1])
	}
	if refs[0].Key() != "C:/Users/joe/Splice/Sounds/Big Pack/Kicks/Kick 01.wav" {
		t.Fatalf("key = %q", refs[0].Key())
	}
}

func TestRewriteLive11(t *testing.T) {
	out, st := Rewrite([]byte(live11), "5", func(r Ref) (Target, bool) {
		switch r.Name {
		case "Kick 01.wav":
			return Target{Rel: "Samples/SPLICE/Big Pack/Kicks/Kick 01.wav", Abs: "D:/Library/Samples/SPLICE/Big Pack/Kicks/Kick 01.wav"}, true
		case "Snare & Clap.aif":
			return Target{Rel: "Samples/SPLICE/Big Pack/Snares/Snare & Clap.wav", Abs: "D:/Library/Samples/SPLICE/Big Pack/Snares/Snare & Clap.wav"}, true
		}
		return Target{}, false
	})
	if st.Refs != 3 || st.Rewritten != 2 || len(st.Unresolved) != 1 || st.Unresolved[0] != "C:/Users/joe/Splice/Sounds/Big Pack/Hats/Missing.wav" {
		t.Fatalf("stats = %+v", st)
	}
	s := string(out)
	for _, want := range []string{
		`<RelativePathType Value="5" />`,
		`<RelativePath Value="Samples/SPLICE/Big Pack/Kicks/Kick 01.wav" />`,
		`<Path Value="D:/Library/Samples/SPLICE/Big Pack/Kicks/Kick 01.wav" />`,
		`<RelativePath Value="Samples/SPLICE/Big Pack/Snares/Snare &amp; Clap.wav" />`,
		`<Path Value="D:/Library/Samples/SPLICE/Big Pack/Snares/Snare &amp; Clap.wav" />`,
		`<OriginalFileSize Value="123456" />`,       // untouched
		`<RelativePath Value="Hats/Missing.wav" />`, // unresolved, untouched
		`<Path Value="C:/Users/joe/Splice/Sounds/Big Pack/Hats/Missing.wav" />`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s\n%s", want, s)
		}
	}
	if strings.Contains(s, "joe/Splice/Sounds/Big Pack/Kicks") {
		t.Error("old kick path survived")
	}
	// everything outside FileRef blocks is untouched
	if !strings.Contains(s, `Creator="Ableton Live 11.3.4"`) || !strings.Contains(s, `<DrumBranchPreset Id="2">`) {
		t.Error("document body damaged")
	}
	// idempotent: rewriting the output with the same resolver changes nothing
	again, _ := Rewrite(out, "5", func(r Ref) (Target, bool) {
		return Target{Rel: r.Rel, Abs: r.Abs}, r.Type == "5"
	})
	if string(again) != s {
		t.Error("rewrite not idempotent")
	}
}

func TestRewriteLive10(t *testing.T) {
	refs := Refs([]byte(live10))
	if len(refs) != 1 || refs[0].Rel != "Kicks/Kick 01.wav" || refs[0].Name != "Kick 01.wav" {
		t.Fatalf("refs = %+v", refs)
	}
	out, st := Rewrite([]byte(live10), "5", func(r Ref) (Target, bool) {
		return Target{Rel: "Samples/SPLICE/Big Pack/Kicks/Kick 01.wav"}, true
	})
	if st.Rewritten != 1 {
		t.Fatalf("stats = %+v", st)
	}
	s := string(out)
	for _, want := range []string{
		`<RelativePathType Value="5" />`,
		`<RelativePathElement Id="0" Dir="Samples" />`,
		`<RelativePathElement Id="1" Dir="SPLICE" />`,
		`<RelativePathElement Id="2" Dir="Big Pack" />`,
		`<RelativePathElement Id="3" Dir="Kicks" />`,
		`<Name Value="Kick 01.wav" />`,
		`<PathHint />`,
		`<FileSize Value="0" />`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s\n%s", want, s)
		}
	}
	if strings.Contains(s, `Dir="joe"`) {
		t.Error("stale path hint survived")
	}
	if got := Refs(out); len(got) != 1 || got[0].Rel != "Samples/SPLICE/Big Pack/Kicks/Kick 01.wav" {
		t.Errorf("re-parse = %+v", got)
	}
}

// A set that plays one sample in forty clips references it forty times;
// the catalog lists it once (the entry is one JSONL line, read on every
// launch).
func TestParseDocDedupesRefs(t *testing.T) {
	block := `<FileRef><RelativePathType Value="3"/><RelativePath Value="Samples/kick.wav"/><Path Value="C:/x/Samples/kick.wav"/></FileRef>`
	other := `<FileRef><RelativePathType Value="3"/><RelativePath Value="Samples/snare.wav"/><Path Value="C:/x/Samples/snare.wav"/></FileRef>`
	xml := "<Ableton>" + strings.Repeat(block, 40) + other + strings.Repeat(block, 3) + "</Ableton>"
	d, err := ParseDoc(Encode([]byte(xml)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Refs) != 2 || d.Refs[0].Name != "kick.wav" || d.Refs[1].Name != "snare.wav" {
		t.Fatalf("refs = %+v", d.Refs)
	}
}
