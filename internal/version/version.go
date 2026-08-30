// Package version carries the build's identity. The release workflow injects
// the real value with -ldflags -X; a source build says "dev" so a mismatch
// between "what I downloaded" and "what I'm running" is visible, not guessed.
package version

var Version = "dev"
