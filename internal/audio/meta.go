// Package audio parses just enough of WAV, AIFF, and FLAC headers to get
// the facts the catalog needs: channels, sample rate, bit depth, and frame
// count. Frame count is what makes output size exact arithmetic instead of
// an estimate, so it is the one field we refuse to guess.
//
// Parsers work on an io.ReadSeeker so they run identically against a local
// file and against a fetched prefix of a remote file (bytes.Reader). If the
// needed header lives beyond the available bytes, they return an error
// rather than a partial answer.
package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
)

type Meta struct {
	Format     string  `json:"format"` // "wav" | "aiff" | "flac"
	Channels   int     `json:"channels"`
	SampleRate int     `json:"sample_rate"`
	BitDepth   int     `json:"bit_depth"`
	Frames     int64   `json:"frames"`
	DurationS  float64 `json:"duration_s"`

	// DualMono: for 2-channel PCM sources, whether L and R carry the same
	// signal (within 1 LSB at 16-bit). nil = not analyzed (remote source,
	// non-PCM, or scanned before this existed); a folded copy of a
	// dual-mono file is lossless, so devices may take one channel with no
	// pad. Derived metadata — computed at scan from the file bytes.
	DualMono *bool `json:"dual_mono,omitempty"`
}

// IsAudioPath reports whether the file extension is one we can parse.
func IsAudioPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".wave", ".aif", ".aiff", ".aifc", ".flac":
		return true
	}
	return false
}

// Parse dispatches on the file's magic bytes when they are recognizable
// (vendors do ship AIFFs named .wav), falling back to the extension.
func Parse(rs io.ReadSeeker, path string) (*Meta, error) {
	var magic [12]byte
	n, _ := io.ReadFull(rs, magic[:])
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if n == len(magic) {
		switch {
		case bytes.Equal(magic[0:4], []byte("RIFF")) && bytes.Equal(magic[8:12], []byte("WAVE")):
			return ParseWAV(rs)
		case bytes.Equal(magic[0:4], []byte("FORM")) &&
			(bytes.Equal(magic[8:12], []byte("AIFF")) || bytes.Equal(magic[8:12], []byte("AIFC"))):
			return ParseAIFF(rs)
		case bytes.Equal(magic[0:4], []byte("fLaC")):
			return ParseFLAC(rs)
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".wave":
		return ParseWAV(rs)
	case ".aif", ".aiff", ".aifc":
		return ParseAIFF(rs)
	case ".flac":
		return ParseFLAC(rs)
	}
	return nil, fmt.Errorf("unsupported audio extension: %s", path)
}

func (m *Meta) finish() *Meta {
	if m.SampleRate > 0 {
		m.DurationS = float64(m.Frames) / float64(m.SampleRate)
	}
	return m
}

// --- WAV (RIFF) ---

func ParseWAV(rs io.ReadSeeker) (*Meta, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(rs, hdr[:]); err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}
	if !bytes.Equal(hdr[0:4], []byte("RIFF")) || !bytes.Equal(hdr[8:12], []byte("WAVE")) {
		return nil, errors.New("wav: not a RIFF/WAVE file")
	}

	m := &Meta{Format: "wav"}
	var haveFmt, haveData bool
	for {
		var ch [8]byte
		if _, err := io.ReadFull(rs, ch[:]); err != nil {
			if haveFmt && !haveData {
				return nil, errors.New("wav: no data chunk within readable bytes")
			}
			return nil, fmt.Errorf("wav: reading chunks: %w", err)
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))

		switch id {
		case "fmt ":
			var f [16]byte
			if _, err := io.ReadFull(rs, f[:]); err != nil {
				return nil, fmt.Errorf("wav: fmt chunk: %w", err)
			}
			m.Channels = int(binary.LittleEndian.Uint16(f[2:4]))
			m.SampleRate = int(binary.LittleEndian.Uint32(f[4:8]))
			m.BitDepth = int(binary.LittleEndian.Uint16(f[14:16]))
			haveFmt = true
			if rest := size - 16; rest > 0 {
				if _, err := rs.Seek(rest+(rest&1), io.SeekCurrent); err != nil {
					return nil, err
				}
			} else if size&1 == 1 {
				if _, err := rs.Seek(1, io.SeekCurrent); err != nil {
					return nil, err
				}
			}
		case "data":
			if !haveFmt {
				return nil, errors.New("wav: data chunk before fmt chunk")
			}
			bytesPerFrame := int64(m.Channels) * int64(m.BitDepth) / 8
			if bytesPerFrame == 0 {
				return nil, errors.New("wav: zero-size frames in fmt chunk")
			}
			m.Frames = size / bytesPerFrame
			haveData = true
			return m.finish(), nil
		default:
			if _, err := rs.Seek(size+(size&1), io.SeekCurrent); err != nil {
				return nil, err
			}
		}
	}
}

// --- AIFF / AIFC ---

func ParseAIFF(rs io.ReadSeeker) (*Meta, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(rs, hdr[:]); err != nil {
		return nil, fmt.Errorf("aiff: %w", err)
	}
	form := string(hdr[8:12])
	if !bytes.Equal(hdr[0:4], []byte("FORM")) || (form != "AIFF" && form != "AIFC") {
		return nil, errors.New("aiff: not a FORM/AIFF file")
	}

	for {
		var ch [8]byte
		if _, err := io.ReadFull(rs, ch[:]); err != nil {
			return nil, fmt.Errorf("aiff: no COMM chunk within readable bytes: %w", err)
		}
		id := string(ch[0:4])
		size := int64(binary.BigEndian.Uint32(ch[4:8]))

		if id != "COMM" {
			if _, err := rs.Seek(size+(size&1), io.SeekCurrent); err != nil {
				return nil, err
			}
			continue
		}

		var c [18]byte
		if _, err := io.ReadFull(rs, c[:]); err != nil {
			return nil, fmt.Errorf("aiff: COMM chunk: %w", err)
		}
		m := &Meta{
			Format:   "aiff",
			Channels: int(binary.BigEndian.Uint16(c[0:2])),
			Frames:   int64(binary.BigEndian.Uint32(c[2:6])),
			BitDepth: int(binary.BigEndian.Uint16(c[6:8])),
		}
		m.SampleRate = int(math.Round(float80(c[8:18])))
		return m.finish(), nil
	}
}

// float80 decodes the 80-bit IEEE 754 extended float AIFF uses for sample
// rate: 1 sign bit, 15 exponent bits, 64 mantissa bits with explicit
// leading 1.
func float80(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:2]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:10])
	if exp == 0 && mant == 0 {
		return 0
	}
	sign := 1.0
	if b[0]&0x80 != 0 {
		sign = -1.0
	}
	return sign * float64(mant) * math.Pow(2, float64(exp-16383-63))
}

// --- FLAC ---

func ParseFLAC(rs io.ReadSeeker) (*Meta, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(rs, hdr[:]); err != nil {
		return nil, fmt.Errorf("flac: %w", err)
	}
	if !bytes.Equal(hdr[:], []byte("fLaC")) {
		return nil, errors.New("flac: bad magic")
	}
	// STREAMINFO is required to be the first metadata block.
	var bh [4]byte
	if _, err := io.ReadFull(rs, bh[:]); err != nil {
		return nil, fmt.Errorf("flac: block header: %w", err)
	}
	if bh[0]&0x7F != 0 {
		return nil, errors.New("flac: first metadata block is not STREAMINFO")
	}
	var si [34]byte
	if _, err := io.ReadFull(rs, si[:]); err != nil {
		return nil, fmt.Errorf("flac: STREAMINFO: %w", err)
	}
	m := &Meta{
		Format:     "flac",
		SampleRate: int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4,
		Channels:   int(si[12]>>1&0x7) + 1,
		BitDepth:   int(si[12]&1)<<4 | int(si[13]>>4) + 1,
		Frames: int64(si[13]&0x0F)<<32 | int64(si[14])<<24 |
			int64(si[15])<<16 | int64(si[16])<<8 | int64(si[17]),
	}
	return m.finish(), nil
}
