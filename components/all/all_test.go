package all_test

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	_ "github.com/rossoctl/context-guru/components/all"
	"github.com/rossoctl/context-guru/config"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

func toolMsg(text string) schemas.ChatMessage {
	t := text
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: &t}}
}

// TestPipelineReducesAndExpands drives the real config->pipeline path with
// cmdfilter + dedup, then recovers an offloaded original via the expand marker.
func TestPipelineReducesAndExpands(t *testing.T) {
	// A pytest-style output the builtin filter should shrink.
	var pytest strings.Builder
	pytest.WriteString("===== test session starts =====\n")
	for i := 0; i < 40; i++ {
		pytest.WriteString("tests/test_module.py::test_case_" + strings.Repeat("x", 3) + " PASSED\n")
	}
	pytest.WriteString("1 failed, 40 passed in 2.10s\n")

	// A big log dump, included twice — dedup should collapse the second.
	dump := strings.Repeat("2026-07-11T10:00:00Z INFO some verbose service log line with detail\n", 30)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Input: []schemas.ChatMessage{
			toolMsg(pytest.String()),
			toolMsg(dump),
			toolMsg(dump),
		},
	}

	cfg, err := config.LoadBytes([]byte("pipeline: [cmdfilter, dedup]\n"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(store.Options{})
	pipe, err := cfg.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &components.Ctx{Ctx: context.Background(), Session: "s1", Store: st}

	before := schema.MessagesTokens(req)
	rr := pipe.Run(req, c)
	after := schema.MessagesTokens(req)

	if after >= before {
		t.Fatalf("pipeline did not reduce tokens: before=%d after=%d", before, after)
	}
	if rr.Saved() <= 0 {
		t.Fatalf("run report shows no savings: %+v", rr)
	}

	// pytest message should now carry an expand marker; recover the original.
	got := schema.MessageText(req.Input[0])
	keys := expand.ParseMarkers(got)
	if len(keys) != 1 {
		t.Fatalf("expected one expand marker in filtered pytest output, got %q", got)
	}
	orig, ok := expand.Resolve(st, keys[0])
	if !ok || !strings.Contains(orig, "test_case_") || !strings.Contains(orig, "PASSED") {
		t.Fatalf("expand did not recover the original pytest output (ok=%v)", ok)
	}

	// dedup should have collapsed the third message (second copy of dump).
	third := schema.MessageText(req.Input[2])
	if !strings.Contains(third, "repeat of") {
		t.Fatalf("dedup did not collapse the duplicate: %q", third)
	}
	dupKeys := expand.ParseMarkers(third)
	if orig, ok := expand.Resolve(st, dupKeys[0]); !ok || orig != dump {
		t.Fatal("dedup marker did not resolve to the original dump")
	}
}

// TestCacheinjectAddsBreakpoint checks the Reformat path adds cache_control on an
// Anthropic request without tripping the never-worse guard.
func TestCacheinjectAddsBreakpoint(t *testing.T) {
	block := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText}
	txt := "system-ish stable prefix content"
	block.Text = &txt
	newer := "newest turn"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{block}}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &newer}},
		},
	}
	cfg, err := config.LoadBytes([]byte("pipeline: [cacheinject]\n"))
	if err != nil {
		t.Fatal(err)
	}
	pipe, _ := cfg.Build(nil)
	c := &components.Ctx{Ctx: context.Background(), Session: "s", Store: store.NewMemory(store.Options{})}
	rr := pipe.Run(req, c)

	if rr.Components[0].Reverted {
		t.Fatalf("cacheinject was reverted (never-worse false positive): %+v", rr.Components[0])
	}
	cc := req.Input[0].Content.ContentBlocks[0].CacheControl
	if cc == nil || cc.Type != schemas.CacheControlTypeEphemeral {
		t.Fatal("cacheinject did not set cache_control on the prefix boundary block")
	}
}
