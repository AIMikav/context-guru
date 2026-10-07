package cheapmodel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidwall/gjson"
)

// THE DEFECT: Claude Code sends max_tokens=32000 with thinking.budget_tokens=31999, and
// CompletePrefixed used to overwrite max_tokens with the fixed PrefixAskMaxTokens (16000)
// without looking at the budget already on the body — 16000 is not greater than 31999, so
// every prefix ask on such a session 400'd with "max_tokens must be greater than
// thinking.budget_tokens" (the live failure this test reproduces offline). See
// thinkingAdjustedMaxTokens's doc comment for why the fix reads the budget rather than
// stripping or resizing the thinking block.
func TestCompletePrefixedRaisesMaxTokensAboveTheThinkingBudget(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"verdicts\":[]}"}],`+
			`"usage":{"input_tokens":40,"output_tokens":10,"cache_read_input_tokens":19595}}`)
	}))
	defer up.Close()

	prefixBody := []byte(`{"model":"claude-sonnet-5","max_tokens":32000,` +
		`"thinking":{"type":"enabled","budget_tokens":31999},` +
		`"messages":[{"role":"user","content":"carry on"}]}`)
	cli := Anthropic{BaseURL: up.URL, Model: "claude-sonnet-5"}
	reply, _, err := cli.CompletePrefixed(context.Background(), prefixBody, "judge")
	if err != nil {
		t.Fatalf("CompletePrefixed: %v", err)
	}
	if reply != `{"verdicts":[]}` {
		t.Fatalf("reply = %q", reply)
	}

	// THE INVARIANT ITSELF: whatever max_tokens went out, it must clear the budget already on
	// the wire, or this test is reproducing nothing and the live 400 would still happen.
	outMax := gjson.GetBytes(got, "max_tokens").Int()
	outBudget := gjson.GetBytes(got, "thinking.budget_tokens").Int()
	if outMax <= outBudget {
		t.Fatalf("sent max_tokens=%d, budget_tokens=%d: still invalid, the provider would 400 "+
			"on this exact body", outMax, outBudget)
	}

	// THE THINKING BLOCK ITSELF MUST BE BYTE-IDENTICAL. A changed thinking.type or
	// thinking.budget_tokens invalidates the cached prefix (see thinkingAdjustedMaxTokens's doc
	// comment), which defeats the entire reason CompletePrefixed exists.
	if got2 := gjson.GetBytes(got, "thinking").String(); got2 != `{"type":"enabled","budget_tokens":31999}` {
		t.Fatalf("the thinking block was changed: %s", got2)
	}

	// EVERYTHING ELSE besides max_tokens, the appended message, and `stream` is untouched.
	if gjson.GetBytes(got, "model").String() != "claude-sonnet-5" {
		t.Fatalf("model changed: %s", got)
	}
	if n := gjson.GetBytes(got, "messages").Array(); len(n) != 2 ||
		n[0].Get("content").String() != "carry on" || n[1].Get("content").String() != "judge" {
		t.Fatalf("messages array was not preserved plus one appended turn: %s", got)
	}
	if gjson.GetBytes(got, "stream").Exists() {
		t.Fatalf("stream was not deleted: %s", got)
	}
}

// THE CEILING BUG, found live on PR #406's review: thinkingAdjustedMaxTokens's raw
// budget_tokens + reply has no upper bound, so a large thinking budget can push max_tokens past
// the MODEL's own output cap -- a new 400 with a different message ("max_tokens: 79999 > 64000,
// which is the maximum allowed number of output tokens") in place of the one this fix exists to
// avoid. Claude Code with CLAUDE_CODE_MAX_OUTPUT_TOKENS=64000 and MAX_THINKING_TOKENS=63999 on
// claude-haiku-4-5 is exactly this shape, and 64000/63999 was measured live: OK, reply "YES",
// cache_read=16230, cache_write=0. See thinkingAdjustedMaxTokens's doc comment for the full
// trade-off this ceiling accepts.
func TestCompletePrefixedCapsAtTheBodysOwnMaxTokensWhenWantWouldExceedTheModelsCap(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"verdicts\":[]}"}],"usage":{}}`)
	}))
	defer up.Close()

	prefixBody := []byte(`{"model":"claude-haiku-4-5","max_tokens":64000,` +
		`"thinking":{"type":"enabled","budget_tokens":63999},` +
		`"messages":[{"role":"user","content":"carry on"}]}`)
	cli := Anthropic{BaseURL: up.URL, Model: "claude-haiku-4-5"}
	if _, _, err := cli.CompletePrefixed(context.Background(), prefixBody, "judge"); err != nil {
		t.Fatalf("CompletePrefixed: %v", err)
	}
	outMax := gjson.GetBytes(got, "max_tokens").Int()
	// The reviewer's exact ask: a result <= 64000 (the model's cap, read via the body's own
	// max_tokens) and > 63999 (still clears the thinking budget).
	if outMax > 64000 || outMax <= 63999 {
		t.Fatalf("max_tokens = %d, want in (63999, 64000]", outMax)
	}
	if got2 := gjson.GetBytes(got, "thinking").String(); got2 != `{"type":"enabled","budget_tokens":63999}` {
		t.Fatalf("the thinking block was changed: %s", got2)
	}
}

// thinking.type "adaptive" carries no budget_tokens at all, and must pass through exactly as
// PrefixAskMaxTokens always has -- this pins that the new budget-reading logic does not touch
// the ordinary case.
func TestCompletePrefixedLeavesAdaptiveThinkingUnaffected(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"verdicts\":[]}"}],"usage":{}}`)
	}))
	defer up.Close()

	prefixBody := []byte(`{"model":"claude-sonnet-5","max_tokens":4096,` +
		`"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"carry on"}]}`)
	cli := Anthropic{BaseURL: up.URL, Model: "claude-sonnet-5"}
	if _, _, err := cli.CompletePrefixed(context.Background(), prefixBody, "judge"); err != nil {
		t.Fatalf("CompletePrefixed: %v", err)
	}
	if got2 := gjson.GetBytes(got, "max_tokens").Int(); got2 != PrefixAskMaxTokens {
		t.Fatalf("max_tokens = %d, want the unadjusted %d (adaptive carries no budget)",
			got2, PrefixAskMaxTokens)
	}
	if got2 := gjson.GetBytes(got, "thinking").String(); got2 != `{"type":"adaptive"}` {
		t.Fatalf("the thinking block was changed: %s", got2)
	}
}

// No thinking block at all -- the ordinary non-Claude-Code case -- must also be unaffected.
func TestCompletePrefixedLeavesNoThinkingUnaffected(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"verdicts\":[]}"}],"usage":{}}`)
	}))
	defer up.Close()

	prefixBody := []byte(`{"model":"claude-sonnet-5","max_tokens":4096,` +
		`"messages":[{"role":"user","content":"carry on"}]}`)
	cli := Anthropic{BaseURL: up.URL, Model: "claude-sonnet-5"}
	if _, _, err := cli.CompletePrefixed(context.Background(), prefixBody, "judge"); err != nil {
		t.Fatalf("CompletePrefixed: %v", err)
	}
	if got2 := gjson.GetBytes(got, "max_tokens").Int(); got2 != PrefixAskMaxTokens {
		t.Fatalf("max_tokens = %d, want the unadjusted %d", got2, PrefixAskMaxTokens)
	}
	if gjson.GetBytes(got, "thinking").Exists() {
		t.Fatalf("a thinking block was invented where the body had none: %s", got)
	}
}

// A CUT-OFF REPLY (stop_reason: "max_tokens") IS RETURNED AS TEXT, NOT AS AN ERROR. The sweep
// (extract_sweep.go) already detects this itself for free -- extract.ParseVerdicts fails on it
// and reports ReplyWasTruncated=true, so the sweep declines under sweep_reply_truncated with no
// extra call. Turning it into an error here instead would route it through sweep_ask_failed,
// which runs a second, full-price fallback by default -- a cost regression a prior version of
// this fix introduced and review caught.
func TestCompletePrefixedReturnsATruncatedReplyRatherThanAnError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"[{\"i\":0"}],`+
			`"stop_reason":"max_tokens","usage":{"input_tokens":40,"output_tokens":16000}}`)
	}))
	defer up.Close()

	prefixBody := []byte(`{"model":"claude-haiku-4-5","max_tokens":32000,` +
		`"thinking":{"type":"enabled","budget_tokens":31999},` +
		`"messages":[{"role":"user","content":"carry on"}]}`)
	cli := Anthropic{BaseURL: up.URL, Model: "claude-haiku-4-5"}
	reply, usage, err := cli.CompletePrefixed(context.Background(), prefixBody, "judge")
	if err != nil {
		t.Fatalf("a truncated reply was turned into an error: %v", err)
	}
	if reply != `[{"i":0` {
		t.Fatalf("reply = %q, want the clipped text passed through unchanged", reply)
	}
	// Usage is still billed: the tokens were spent whether or not the reply parses.
	if usage.Output != 16000 {
		t.Fatalf("usage.Output = %d, want 16000", usage.Output)
	}
}

// thinkingAdjustedMaxTokens unit-level: pins the arithmetic directly, independent of the HTTP
// plumbing above.
func TestThinkingAdjustedMaxTokens(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		want       int
		wantCapped bool
	}{
		{"enabled with a large budget, no orig max_tokens on the body",
			`{"thinking":{"type":"enabled","budget_tokens":31999}}`, 31999 + PrefixAskMaxTokens, false},
		{"enabled with a small budget", `{"thinking":{"type":"enabled","budget_tokens":10}}`,
			10 + PrefixAskMaxTokens, false},
		{"adaptive", `{"thinking":{"type":"adaptive"}}`, PrefixAskMaxTokens, false},
		{"disabled", `{"thinking":{"type":"disabled"}}`, PrefixAskMaxTokens, false},
		{"absent", `{}`, PrefixAskMaxTokens, false},
		// Reviewer's minor note: "enabled" with budget_tokens missing is 0 + reply. The API would
		// reject such a body anyway (thinking.enabled requires a budget), so this is not a shape
		// CompletePrefixed must defend against — just pinned so the fall-through reads as a
		// choice, not an oversight.
		{"enabled with budget_tokens missing", `{"thinking":{"type":"enabled"}}`, PrefixAskMaxTokens, false},
		// The reviewer's live-measured 64000/63999 shape: the raw want (63999+16000=79999)
		// exceeds the model's cap, so the body's own max_tokens (64000, measured live as OK --
		// cache_read=16230, cache_write=0) is used instead.
		{"orig max_tokens caps a want that would exceed the model's output limit",
			`{"thinking":{"type":"enabled","budget_tokens":63999},"max_tokens":64000}`, 64000, true},
		// orig == budget is NOT a valid ceiling (it would equal the budget, violating the
		// provider's strict ">" requirement), so the strict "orig > budget" guard must reject it
		// and fall through to the computed want.
		{"orig max_tokens equal to the budget is not used as a ceiling",
			`{"thinking":{"type":"enabled","budget_tokens":31999},"max_tokens":31999}`,
			31999 + PrefixAskMaxTokens, false},
		// orig below the computed want but ALSO below the budget: not a valid ceiling either
		// (an orig that doesn't even clear the budget proves nothing), so it must not be used.
		{"orig max_tokens below the budget is not used as a ceiling",
			`{"thinking":{"type":"enabled","budget_tokens":31999},"max_tokens":100}`,
			31999 + PrefixAskMaxTokens, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, capped := thinkingAdjustedMaxTokens([]byte(tc.body), PrefixAskMaxTokens)
			if got != tc.want {
				t.Errorf("thinkingAdjustedMaxTokens = %d, want %d", got, tc.want)
			}
			if capped != tc.wantCapped {
				t.Errorf("capped = %v, want %v", capped, tc.wantCapped)
			}
		})
	}
}
