package location

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// SSH runs everything through the system ssh binary, which brings
// ~/.ssh/config, agents, and keys along for free. Listing and hashing
// execute remotely so only manifests cross the network; file bytes are
// streamed on demand.
type SSH struct {
	name string
	host string
	root string
}

func (s *SSH) Name() string { return s.name }

func (s *SSH) command(ctx context.Context, remote string, stdin io.Reader) *exec.Cmd {
	// BatchMode: fail fast instead of prompting for a password mid-scan.
	// ControlMaster multiplexing: scans issue one ssh exec per audio file
	// for header reads — without connection reuse that is a full handshake
	// each time, which turns a big scan from minutes into hours.
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/mtunes-%r@%h-%p",
		"-o", "ControlPersist=120",
		s.host, remote)
	cmd.Stdin = stdin
	return cmd
}

func (s *SSH) List(ctx context.Context) ([]File, error) {
	// GNU find prints size, mtime, and root-relative path in one pass.
	// -printf %P is the root-relative path; hidden files and dirs skipped
	// to match Local's behavior. `command` bypasses shell aliases/functions
	// on the remote (e.g. fish users wrapping find with fd) in both fish
	// and POSIX shells.
	remote := fmt.Sprintf(
		`command find %s -name '.*' -prune -o -type f -printf '%%s\t%%T@\t%%P\n'`,
		shellQuote(s.root),
	)
	cmd := s.command(ctx, remote, nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var files []File
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		size, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		mtimeF, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		files = append(files, File{Path: parts[2], Size: size, MTime: int64(mtimeF)})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ssh %s: %w: %s", s.host, err, strings.TrimSpace(stderr.String()))
	}
	return files, nil
}

func (s *SSH) HashAll(ctx context.Context, paths []string, progress func()) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	// NUL-delimited paths on stdin; sha256sum runs remotely so sample
	// bytes never cross the network for cataloging.
	var stdin bytes.Buffer
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte(0)
	}
	// xargs execs sha256sum directly (no shell), so only xargs itself needs
	// the alias bypass.
	remote := fmt.Sprintf(`cd %s && command xargs -0 sha256sum --`, shellQuote(s.root))
	cmd := s.command(ctx, remote, &stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sums := make(map[string]string, len(paths))
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// sha256sum escapes filenames containing backslash or newline by
		// prefixing the line with '\'. Newlines can't appear (we supplied
		// the names), so only unescape backslashes.
		escaped := strings.HasPrefix(line, "\\")
		line = strings.TrimPrefix(line, "\\")
		if len(line) < 66 { // 64 hex + two-space separator
			continue
		}
		sum, name := line[:64], line[66:]
		if escaped {
			name = strings.ReplaceAll(name, `\\`, `\`)
		}
		sums[name] = sum
		if progress != nil {
			progress()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ssh %s: sha256sum: %w: %s", s.host, err, strings.TrimSpace(stderr.String()))
	}
	if len(sums) != len(paths) {
		return nil, fmt.Errorf("ssh %s: hashed %d of %d files (remote errors: %s)",
			s.host, len(sums), len(paths), strings.TrimSpace(stderr.String()))
	}
	return sums, nil
}

// ReadPrefixes streams the first n bytes of many files over ONE remote
// invocation: a sh loop emits alternating path and base64-header lines.
// At catalog scale (150k+ audio files) per-file round trips are the
// difference between minutes and hours, even with connection reuse.
// Paths containing newlines are not supported (List already assumes none).
func (s *SSH) ReadPrefixes(ctx context.Context, paths []string, n int64, fn func(path string, data []byte)) error {
	var stdin bytes.Buffer
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte('\n')
	}
	// The remote loop is POSIX sh; the user's login shell may be fish, so
	// wrap it in an explicit sh -c.
	script := fmt.Sprintf(
		`cd %s && while IFS= read -r f; do printf '%%s\n' "$f"; head -c %d -- "$f" 2>/dev/null | base64 | tr -d '\n'; printf '\n'; done`,
		shellQuote(s.root), n)
	cmd := s.command(ctx, "sh -c "+shellQuote(script), &stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for sc.Scan() {
		path := sc.Text()
		if !sc.Scan() {
			break
		}
		data, err := base64.StdEncoding.DecodeString(sc.Text())
		if err != nil {
			continue // damaged frame; caller falls back to ReadPrefix
		}
		fn(path, data)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ssh %s: batch header read: %w: %s", s.host, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s *SSH) ReadPrefix(ctx context.Context, rel string, n int64) ([]byte, error) {
	remote := fmt.Sprintf(`command head -c %d -- %s`, n, shellQuote(s.root+"/"+rel))
	cmd := s.command(ctx, remote, nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh %s: head %s: %w: %s", s.host, rel, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (s *SSH) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	remote := fmt.Sprintf(`command cat -- %s`, shellQuote(s.root+"/"+rel))
	cmd := s.command(ctx, remote, nil)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdReader{ReadCloser: out, cmd: cmd}, nil
}

// cmdReader closes the pipe and reaps the ssh process.
type cmdReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (c *cmdReader) Close() error {
	c.ReadCloser.Close()
	return c.cmd.Wait()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
