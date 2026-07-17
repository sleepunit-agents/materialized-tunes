package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// buildWAV synthesizes a minimal valid WAV header + silent data.
func buildWAV(channels, rate, depth int, frames int, junkChunk bool) []byte {
	dataSize := frames * channels * depth / 8
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(0)) // riff size, unchecked
	b.WriteString("WAVE")

	if junkChunk {
		// odd chunk size — real vendor files do this — followed by its pad byte
		b.WriteString("LIST")
		binary.Write(&b, binary.LittleEndian, uint32(11))
		b.Write(make([]byte, 11))
		b.WriteByte(0)
	}

	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*channels*depth/8))
	binary.Write(&b, binary.LittleEndian, uint16(channels*depth/8))
	binary.Write(&b, binary.LittleEndian, uint16(depth))

	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataSize))
	b.Write(make([]byte, dataSize))
	return b.Bytes()
}

func TestParseWAV(t *testing.T) {
	data := buildWAV(2, 44100, 16, 44100*2, false) // 2s stereo
	m, err := ParseWAV(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Channels != 2 || m.SampleRate != 44100 || m.BitDepth != 16 {
		t.Errorf("format = %d ch / %d Hz / %d bit", m.Channels, m.SampleRate, m.BitDepth)
	}
	if m.Frames != 88200 || math.Abs(m.DurationS-2.0) > 1e-9 {
		t.Errorf("frames = %d, duration = %f", m.Frames, m.DurationS)
	}
}

func TestParseWAVWithJunkChunks(t *testing.T) {
	data := buildWAV(1, 48000, 24, 48000*5, true) // 5s mono 24-bit, LIST chunk first
	m, err := ParseWAV(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Channels != 1 || m.SampleRate != 48000 || m.BitDepth != 24 || m.Frames != 240000 {
		t.Errorf("got %+v", m)
	}
}

func TestParseWAVTruncatedPrefix(t *testing.T) {
	// A "remote prefix" that ends before the data chunk: must error, never
	// return a partial answer.
	data := buildWAV(2, 44100, 16, 44100, false)
	_, err := ParseWAV(bytes.NewReader(data[:20]))
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func buildAIFF(channels, rate, depth int, frames int) []byte {
	var b bytes.Buffer
	b.WriteString("FORM")
	binary.Write(&b, binary.BigEndian, uint32(0))
	b.WriteString("AIFF")
	b.WriteString("COMM")
	binary.Write(&b, binary.BigEndian, uint32(18))
	binary.Write(&b, binary.BigEndian, uint16(channels))
	binary.Write(&b, binary.BigEndian, uint32(frames))
	binary.Write(&b, binary.BigEndian, uint16(depth))
	b.Write(encodeFloat80(float64(rate)))
	return b.Bytes()
}

// encodeFloat80 encodes a positive float as 80-bit IEEE extended.
func encodeFloat80(f float64) []byte {
	out := make([]byte, 10)
	if f == 0 {
		return out
	}
	exp := 0
	for f < (1 << 62) {
		f *= 2
		exp++
	}
	mant := uint64(f)
	binary.BigEndian.PutUint16(out[0:2], uint16(16383+63+(62-63)-exp+1))
	binary.BigEndian.PutUint64(out[2:10], mant)
	return out
}

func TestFloat80RoundTrip(t *testing.T) {
	for _, rate := range []float64{44100, 48000, 22050, 96000, 8000} {
		got := float80(encodeFloat80(rate))
		if math.Abs(got-rate) > 0.01 {
			t.Errorf("float80 round-trip: want %f, got %f", rate, got)
		}
	}
}

func TestParseAIFF(t *testing.T) {
	data := buildAIFF(2, 44100, 16, 132300) // 3s stereo
	m, err := ParseAIFF(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Channels != 2 || m.SampleRate != 44100 || m.BitDepth != 16 || m.Frames != 132300 {
		t.Errorf("got %+v", m)
	}
	if math.Abs(m.DurationS-3.0) > 1e-9 {
		t.Errorf("duration = %f", m.DurationS)
	}
}

func buildFLAC(channels, rate, depth int, frames int64) []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	b.WriteByte(0x80) // last block, type 0 = STREAMINFO
	b.Write([]byte{0, 0, 34})
	si := make([]byte, 34)
	si[10] = byte(rate >> 12)
	si[11] = byte(rate >> 4)
	si[12] = byte(rate&0xF)<<4 | byte(channels-1)<<1 | byte((depth-1)>>4&1)
	si[13] = byte((depth-1)&0xF)<<4 | byte(frames>>32&0xF)
	si[14] = byte(frames >> 24)
	si[15] = byte(frames >> 16)
	si[16] = byte(frames >> 8)
	si[17] = byte(frames)
	b.Write(si)
	return b.Bytes()
}

func TestParseFLAC(t *testing.T) {
	data := buildFLAC(2, 44100, 16, 220500) // 5s stereo
	m, err := ParseFLAC(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Channels != 2 || m.SampleRate != 44100 || m.BitDepth != 16 || m.Frames != 220500 {
		t.Errorf("got %+v", m)
	}
	if math.Abs(m.DurationS-5.0) > 1e-9 {
		t.Errorf("duration = %f", m.DurationS)
	}
}

func TestParseFLAC24Bit(t *testing.T) {
	data := buildFLAC(1, 96000, 24, 96000)
	m, err := ParseFLAC(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Channels != 1 || m.SampleRate != 96000 || m.BitDepth != 24 || m.Frames != 96000 {
		t.Errorf("got %+v", m)
	}
}
