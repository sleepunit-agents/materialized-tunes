package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// dualMonoTolerance is the per-sample |L−R| allowed, expressed at 16-bit
// resolution: one LSB. Scaled up for 24-bit. Dither and rounding differ
// between channels of a real dual-mono bounce by at most this; a real
// stereo image blows past it in the first few frames.
const dualMonoTolerance16 = 1

// AnalyzeDualMono reads a whole 2-channel integer-PCM WAV or AIFF and
// reports whether both channels carry the same signal. Errors for anything
// it can't decode (compressed, float, odd depths, mono, truncated) — the
// caller records "unknown", never "false", for those.
func AnalyzeDualMono(rs io.ReadSeeker) (bool, error) {
	var magic [12]byte
	if _, err := io.ReadFull(rs, magic[:]); err != nil {
		return false, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	switch {
	case string(magic[0:4]) == "RIFF" && string(magic[8:12]) == "WAVE":
		return dualMonoWAV(rs)
	case string(magic[0:4]) == "FORM" && (string(magic[8:12]) == "AIFF" || string(magic[8:12]) == "AIFC"):
		return dualMonoAIFF(rs)
	}
	return false, errors.New("dual-mono: not a WAV/AIFF")
}

func dualMonoWAV(rs io.ReadSeeker) (bool, error) {
	if _, err := rs.Seek(12, io.SeekStart); err != nil {
		return false, err
	}
	var channels, depth, format int
	for {
		var ch [8]byte
		if _, err := io.ReadFull(rs, ch[:]); err != nil {
			return false, err
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "fmt ":
			var f [16]byte
			if _, err := io.ReadFull(rs, f[:]); err != nil {
				return false, err
			}
			format = int(binary.LittleEndian.Uint16(f[0:2]))
			channels = int(binary.LittleEndian.Uint16(f[2:4]))
			depth = int(binary.LittleEndian.Uint16(f[14:16]))
			if format == 0xFFFE && size >= 40 { // WAVE_FORMAT_EXTENSIBLE: sub-format GUID's first 2 bytes
				var ext [24]byte
				if _, err := io.ReadFull(rs, ext[:]); err != nil {
					return false, err
				}
				format = int(binary.LittleEndian.Uint16(ext[8:10]))
				size -= 24
			}
			if rest := size - 16; rest > 0 {
				if _, err := rs.Seek(rest+(rest&1), io.SeekCurrent); err != nil {
					return false, err
				}
			} else if size&1 == 1 {
				rs.Seek(1, io.SeekCurrent)
			}
		case "data":
			if format != 1 { // integer PCM only
				return false, errors.New("dual-mono: not integer PCM")
			}
			return compareChannels(rs, size, channels, depth, binary.LittleEndian)
		default:
			if _, err := rs.Seek(size+(size&1), io.SeekCurrent); err != nil {
				return false, err
			}
		}
	}
}

func dualMonoAIFF(rs io.ReadSeeker) (bool, error) {
	if _, err := rs.Seek(12, io.SeekStart); err != nil {
		return false, err
	}
	var channels, depth int
	compressed := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(rs, ch[:]); err != nil {
			return false, err
		}
		id := string(ch[0:4])
		size := int64(binary.BigEndian.Uint32(ch[4:8]))
		switch id {
		case "COMM":
			var c [18]byte
			if _, err := io.ReadFull(rs, c[:]); err != nil {
				return false, err
			}
			channels = int(binary.BigEndian.Uint16(c[0:2]))
			depth = int(binary.BigEndian.Uint16(c[6:8]))
			if size > 18 { // AIFC: compression type follows
				var ct [4]byte
				if _, err := io.ReadFull(rs, ct[:]); err != nil {
					return false, err
				}
				if s := string(ct[:]); s != "NONE" && s != "sowt" && s != "twos" {
					compressed = true
				}
				if s := string(ct[:]); s == "sowt" {
					// little-endian AIFC; rare, and our comparator assumes big-endian for AIFF
					compressed = true
				}
				rest := size - 22
				if _, err := rs.Seek(rest+(size&1), io.SeekCurrent); err != nil {
					return false, err
				}
			} else if size&1 == 1 {
				rs.Seek(1, io.SeekCurrent)
			}
		case "SSND":
			if compressed {
				return false, errors.New("dual-mono: compressed AIFC")
			}
			var hdr [8]byte // offset, blockSize
			if _, err := io.ReadFull(rs, hdr[:]); err != nil {
				return false, err
			}
			off := int64(binary.BigEndian.Uint32(hdr[0:4]))
			if _, err := rs.Seek(off, io.SeekCurrent); err != nil {
				return false, err
			}
			return compareChannels(rs, size-8-off, channels, depth, binary.BigEndian)
		default:
			if _, err := rs.Seek(size+(size&1), io.SeekCurrent); err != nil {
				return false, err
			}
		}
	}
}

// compareChannels streams n bytes of interleaved 2-channel PCM and reports
// whether every frame's channels agree within tolerance.
func compareChannels(r io.Reader, n int64, channels, depth int, order binary.ByteOrder) (bool, error) {
	if channels != 2 {
		return false, errors.New("dual-mono: not 2 channels")
	}
	if depth != 16 && depth != 24 && depth != 32 {
		return false, errors.New("dual-mono: unsupported bit depth")
	}
	bps := depth / 8
	frame := bps * 2
	tol := int64(dualMonoTolerance16) << uint(depth-16)
	br := bufio.NewReaderSize(io.LimitReader(r, n), 1<<20)
	buf := make([]byte, frame*4096)
	sample := func(b []byte) int64 {
		switch depth {
		case 16:
			return int64(int16(order.Uint16(b)))
		case 24:
			var v uint32
			if order == binary.LittleEndian {
				v = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
			} else {
				v = uint32(b[2]) | uint32(b[1])<<8 | uint32(b[0])<<16
			}
			return int64(int32(v<<8) >> 8)
		default:
			return int64(int32(order.Uint32(b)))
		}
	}
	for {
		got, err := io.ReadFull(br, buf)
		if err == io.ErrUnexpectedEOF {
			got -= got % frame
		} else if err == io.EOF {
			return true, nil
		} else if err != nil {
			return false, err
		}
		for i := 0; i+frame <= got; i += frame {
			l, rr := sample(buf[i:i+bps]), sample(buf[i+bps:i+frame])
			d := l - rr
			if d < 0 {
				d = -d
			}
			if d > tol {
				return false, nil
			}
		}
		if got < len(buf) {
			return true, nil
		}
	}
}
