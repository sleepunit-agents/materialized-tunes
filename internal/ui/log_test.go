package ui

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A page-side failure posted to /api/log lands in the process log with
// its kind, where it happened, what the page was doing, and the trace —
// the file the user is asked to send is the file the page's errors go to.
func TestClientLogLandsInProcessLog(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	s := &Server{}
	body := `{"where":"the page","message":"The play() request was interrupted by a new load request.","stack":"AbortError: The play() request was interrupted\n    at playFile (app.js:390)","fatal":true,"screen":"plan","player":"A/P1/kick.wav","trace":["12:00:00.000 key ArrowDown","12:00:00.120 play A/P1/kick.wav"]}`
	w := httptest.NewRecorder()
	s.clientLog(w, httptest.NewRequest(http.MethodPost, "/api/log", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := buf.String()
	for _, want := range []string{
		"CLIENT FATAL in the page: The play() request was interrupted by a new load request.",
		`(screen plan, playing "A/P1/kick.wav")`,
		"stack: AbortError",
		"trace (oldest first):",
		"key ArrowDown",
		"play A/P1/kick.wav",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}

	w = httptest.NewRecorder()
	s.clientLog(w, httptest.NewRequest(http.MethodGet, "/api/log", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d, want 405", w.Code)
	}
}
