package loop

// The retry policy's two terminal behaviours: the Retry-After header
// beats the backoff table, and a request that never stops failing is
// retried exactly maxRetries times after the initial attempt before the
// last error surfaces verbatim. Both are timed with a lower bound only:
// the assertion is that the override was honoured, never how fast the
// machine was.

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestLoopHonoursRetryAfter scripts a 429 carrying Retry-After: 2 and
// then a good response. The first backoff slot is 1s, so a run that
// sleeps at least 2s before succeeding proved the header path rather
// than the backoff table.
func TestLoopHonoursRetryAfter(t *testing.T) {
	root := t.TempDir()
	p := newScriptedProvider(t,
		scriptedResponse{status: 429, body: `{"error":{"message":"slow down"}}`, headers: map[string]string{"Retry-After": "2"}},
		anthropicText("made it through", 10, 5),
	)

	started := time.Now()
	_, out, err := loopRun(t, p, root)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(p.received()); got != 2 {
		t.Errorf("the provider saw %d requests, want 2 (429, 200)", got)
	}
	if elapsed < 2*time.Second {
		t.Errorf("the retry slept %s, want at least the 2s Retry-After (the backoff slot is 1s)", elapsed)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the final message was not written: %v", err)
	}
	if final := string(data); final != "made it through" {
		t.Errorf("final message = %q, want the response after the retry", final)
	}
}

// TestLoopRetriesAreExhaustedAfterFourRetries scripts a provider that
// always answers 500 with Retry-After: 1 — the override keeps every
// retry's sleep at 1s, so the whole policy costs ~4s instead of 15s of
// raw backoff AND every retry proves the header path. After the initial
// attempt plus maxRetries retries the run must fail with the last
// response's status verbatim.
func TestLoopRetriesAreExhaustedAfterFourRetries(t *testing.T) {
	root := t.TempDir()
	script := make([]scriptedResponse, 0, maxRetries+1)
	for i := 0; i < maxRetries+1; i++ {
		script = append(script, scriptedResponse{
			status:  500,
			body:    `{"error":{"message":"still down"}}`,
			headers: map[string]string{"Retry-After": "1"},
		})
	}
	p := newScriptedProvider(t, script...)

	started := time.Now()
	_, _, err := loopRun(t, p, root)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Run succeeded against a provider that only ever 500s")
	}
	if !strings.Contains(err.Error(), "pika loop: anthropic 500") {
		t.Errorf("error %q does not name the 500 status verbatim", err)
	}
	if got := len(p.received()); got != maxRetries+1 {
		t.Errorf("the provider saw %d requests, want %d (initial + %d retries)", got, maxRetries+1, maxRetries)
	}
	if elapsed < time.Duration(maxRetries)*time.Second {
		t.Errorf("the retries slept %s in total, want at least %ds of Retry-After", elapsed, maxRetries)
	}
}
