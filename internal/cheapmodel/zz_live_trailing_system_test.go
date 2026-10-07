package cheapmodel

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Live proof, on the PLAIN gateway (never Context Guru), that a prefix ask over a request whose
// own last message is a role:"system" reminder — the shape this fix exists for — no longer 400s,
// and still reads the cache it is supposed to. Gated like the other live tests in this package
// (CG_LIVE=1 + CG_BASE/CG_TOKEN/CG_MODEL): skipped by default, so the ordinary unit suite never
// needs network access or an API key.
func TestLiveCompletePrefixedWithTrailingSystemMessage(t *testing.T) {
	if os.Getenv("CG_LIVE") == "" {
		t.Skip("set CG_LIVE=1 CG_BASE=... CG_TOKEN=... CG_MODEL=... to run")
	}
	model := os.Getenv("CG_MODEL")
	if model == "" {
		model = "claude-sonnet-5[1m]"
	}
	cli := Anthropic{BaseURL: os.Getenv("CG_BASE"), APIKey: os.Getenv("CG_TOKEN"), Model: model, AuthScheme: "bearer"}

	// A transcript large enough to clear the provider's minimum cacheable prefix on a
	// sonnet-class model (1,024 provider tokens; see cachecontrol.go), marked with the SAME
	// cache_control breakpoint Claude Code places on its own final message.
	convo := strings.Repeat(
		"the handler in src/mod/file.py returns 500 when the payload is missing a trailing newline; "+
			"pytest tests/test_handler.py reports three failures in dispatch\n", 400)
	body := map[string]any{
		"model":      model,
		"max_tokens": 64,
		"messages": []any{
			map[string]any{"role": "user", "content": convo},
			map[string]any{"role": "assistant", "content": "Noted; investigating."},
			map[string]any{"role": "user", "content": "continue"},
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "text", "text": "<system-reminder>be careful</system-reminder>",
					"cache_control": map[string]any{"type": "ephemeral"}},
			}},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	// First call writes the cache entry. insertAskMessage already applies here too — this exact
	// path is what a live sweep/summarizer call takes, trailing system message and all.
	if _, _, err := cli.CompletePrefixed(context.Background(), raw, "first pass: warm the cache"); err != nil {
		t.Fatalf("first call (writes the entry): %v", err)
	}

	// Second call, over the IDENTICAL prefixBody: this is the live assertion that matters. Before
	// this fix, EVERY prefix ask on this shape 400'd ("role 'system' must precede an 'assistant'
	// message or end the array"); a plain err == nil here is itself the proof the defect is gone.
	_, usage, err := cli.CompletePrefixed(context.Background(), raw, "second pass: read the cache")
	if err != nil {
		t.Fatalf("second call over a trailing-system prefix: %v", err)
	}
	t.Logf("usage: cache_read=%d cache_write=%d fresh=%d output=%d",
		usage.CacheRead, usage.CacheWrite, usage.Fresh, usage.Output)
	if usage.CacheRead == 0 {
		t.Errorf("cache_read == 0 on the second call: the fix may be reading nothing from cache " +
			"(informational — a cold start or a load-balanced miss on a shared gateway can also " +
			"produce this; re-run if this fires)")
	}
}
