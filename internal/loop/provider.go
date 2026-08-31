package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Choaterboater/pika/internal/redact"
)

// provider is one vendor: which wire shape, where to send it, and which
// environment variables carry the key and a base-URL override. The
// base-URL override is the testing seam and it is the only one: a test
// points it at a local httptest server and the whole suite stays
// LLM-free.
type provider struct {
	name       string // "anthropic" | "openai" | "openrouter"
	baseURL    string // default endpoint
	keyEnv     string // env var carrying the API key
	baseURLEnv string // env var overriding baseURL
	model      string // default model when the contract sets none
	client     client
}

// providers is the provider table. The clients it holds are prototypes:
// the key is not known until NewRunner and the base-URL override not
// until Run, so the concrete client is bound only then (bindClient).
var providers = map[string]provider{
	"anthropic": {
		name:       "anthropic",
		baseURL:    "https://api.anthropic.com",
		keyEnv:     "ANTHROPIC_API_KEY",
		baseURLEnv: "ANTHROPIC_BASE_URL",
		model:      "claude-sonnet-4-5",
		client:     anthropicClient{name: "anthropic"},
	},
	"openai": {
		name:       "openai",
		baseURL:    "https://api.openai.com/v1",
		keyEnv:     "OPENAI_API_KEY",
		baseURLEnv: "OPENAI_BASE_URL",
		model:      "gpt-5-codex",
		client:     openaiClient{name: "openai"},
	},
	"openrouter": {
		name:       "openrouter",
		baseURL:    "https://openrouter.ai/api/v1",
		keyEnv:     "OPENROUTER_API_KEY",
		baseURLEnv: "OPENROUTER_BASE_URL",
		model:      "anthropic/claude-sonnet-4-5",
		client:     openaiClient{name: "openrouter"},
	},
}

// bindClient returns the provider table's prototype client bound to the
// resolved key and base URL. A client it does not know — a test's fake —
// is returned unchanged.
func bindClient(c client, key, baseURL string) client {
	switch c := c.(type) {
	case anthropicClient:
		c.key, c.baseURL = key, baseURL
		return c
	case openaiClient:
		c.key, c.baseURL = key, baseURL
		return c
	default:
		return c
	}
}

// client is one wire shape: how a message, a tool call and a token count
// travel to one family of provider API. The turn loop above it is
// provider-agnostic; only the projection differs.
type client interface {
	// complete exchanges the conversation so far for one response.
	complete(ctx context.Context, req request) (response, error)
}

type request struct {
	system   string
	messages []message
	tools    []tool
	model    string
	effort   string
}

type response struct {
	text  []string   // text parts, in order
	calls []toolCall // tool calls, in order
	usage usage
}

type message struct {
	role  string // "user" | "assistant"
	parts []part
}

// part is one content block: exactly one of the three fields is set.
type part struct {
	text   string
	call   *toolCall
	result *toolResult
}

type toolCall struct {
	id, name string
	input    json.RawMessage
}

type toolResult struct {
	id, output string
	isError    bool
}

type tool struct {
	name, description string
	schema            any
}

type usage struct{ in, out int }

// httpClient is shared by both wire clients. Its timeout is zero because
// the timeout is the request's, not the client's: the turn loop wraps
// every complete call in its own 5-minute context.
var httpClient = &http.Client{Timeout: 0}

// maxRetries bounds the retries after the initial attempt — the plan's
// "maxAttempts = 4" — so one request tries at most five times, sleeping
// backoff[0..3] (1s, 2s, 4s, 8s) before each retry. Retries apply to
// responses, never to effects: 429, 5xx and transport errors are safe to
// retry; a 4xx is a fact to surface verbatim, and a timed-out call is
// never repeated.
const maxRetries = 4

var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// maxErrorBody bounds a provider error body inside an error string.
const maxErrorBody = 2 * 1024

// httpError is a non-2xx response after the retry policy has had its say.
// The body is redacted and tail-truncated before it is stored, because a
// provider error page is exactly where a leaked key or a path would
// otherwise travel to a terminal.
type httpError struct {
	status string // the response's Status, e.g. "401 Unauthorized"
	body   string // redacted, tail-truncated to 2 KiB
}

func (e *httpError) Error() string { return e.status + ": " + e.body }

// wrapProviderError gives a provider error its verbatim surface form —
// "pika loop: <provider> <status>: <body>" — while a context error
// (cancellation, the turn's timeout) passes through untouched so the turn
// loop can recognise it.
func wrapProviderError(name string, err error) error {
	var he *httpError
	if errors.As(err, &he) {
		return fmt.Errorf("pika loop: %s %s: %s", name, he.status, he.body)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("pika loop: %s: %w", name, err)
}

// doJSON encodes body, sends it, and decodes the response into out,
// applying the shared retry policy: 429, 5xx and transport errors retry
// up to maxRetries times after the initial attempt, honouring a
// Retry-After header under 60s; every other 4xx and any context expiry
// returns at once.
func doJSON(ctx context.Context, method, url string, headers map[string]string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	var lastErr error
	var retryAfter time.Duration
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			delay := backoff[attempt-1]
			if retryAfter > 0 {
				delay = retryAfter
			}
			if err := sleep(ctx, delay); err != nil {
				return err
			}
		}
		retryAfter = 0
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			// A transport error is retryable — unless the request's own
			// context ended it, which is never retried.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
		} else {
			data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			status, code := resp.Status, resp.StatusCode
			ra := resp.Header.Get("Retry-After")
			resp.Body.Close()
			switch {
			case err != nil:
				lastErr = err
			case code >= 200 && code < 300:
				if out == nil {
					return nil
				}
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("decode response: %w", err)
				}
				return nil
			default:
				lastErr = &httpError{status: status, body: redactBody(data)}
				if code != http.StatusTooManyRequests && code < 500 {
					// Every other 4xx is a fact to surface verbatim,
					// never a reason to retry.
					return lastErr
				}
				retryAfter = parseRetryAfter(ra)
			}
		}
		if attempt == maxRetries {
			return lastErr
		}
	}
}

// sleep waits out a retry delay, waking early if the request's context
// ends — a cancelled or timed-out turn never finishes its backoff.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// parseRetryAfter honours a Retry-After header only when it is a whole
// number of seconds under 60; anything else falls back to the backoff
// table.
func parseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 || n >= 60 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// redactBody prepares a provider error body for an error string: redacted
// and tail-truncated to 2 KiB.
func redactBody(b []byte) string {
	s := redact.Apply(string(b))
	if len(s) > maxErrorBody {
		s = s[len(s)-maxErrorBody:]
	}
	return s
}
