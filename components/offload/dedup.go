package offload

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
)

func init() { components.Register("dedup", newDedup) }

// Dedup replaces a tool output that is byte-identical to an earlier one in the
// same request with a short pointer + expand marker, stashing the original.
// Exact-match only in v1; near-duplicate (similarity threshold) is deferred.
type Dedup struct {
	minTokens int
	mode      markerMode
}

type dedupConfig struct {
	MinTokens  int    `yaml:"min_tokens"`
	MarkerMode string `yaml:"marker_mode"` // full (default) | summary | off
}

func newDedup(raw []byte) (components.Component, error) {
	cfg := dedupConfig{MinTokens: 100}
	if err := components.Decode(raw, &cfg); err != nil {
		return nil, err
	}
	return &Dedup{minTokens: cfg.MinTokens, mode: parseMarkerMode(cfg.MarkerMode)}, nil
}

func (Dedup) Name() string                 { return "dedup" }
func (Dedup) Enabled(*components.Ctx) bool { return true }

func (d *Dedup) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	seen := map[string]int{} // content hash -> first message index
	// The call that produced each tool result, so the note can name the earlier command
	// rather than the earlier position. Nil-safe: an unpaired result reads the zero value.
	pairs := schema.ToolCalls(req)
	var keys []string
	changed := 0
	for i := range req.Input {
		m := &req.Input[i]
		if m.Role != schemas.ChatMessageRoleTool {
			continue
		}
		if !schema.Rewritable(*m) {
			rep.Gate("non_text_blocks") // would be dropped by a text rewrite
			continue
		}
		content := schema.MessageText(*m)
		if content == "" || schema.TextTokens(content) < d.minTokens {
			rep.Gate("below_min_tokens")
			continue
		}
		if gate, skip := skipReduce(c, content); skip {
			rep.Gate(gate) // don't re-reduce; the gate says which reason
			continue
		}
		h := hashKey(content)
		first, dup := seen[h]
		if !dup {
			seen[h] = i
			rep.Gate("no_earlier_identical_output")
			continue
		}
		// Later duplicate: collapse to a pointer (stash+marker in full mode). The note
		// names the earlier CALL where one is recoverable, never the earlier position.
		note := dedupNote(pairs[first])
		newText, key, eff, ok := tryMark(c, d.mode, content, " [full output: call "+expand.ToolName+"]",
			func(tok string) string { return note + tok })
		if !ok {
			rep.Gate("marker_no_win") // pointer+marker wouldn't shrink this duplicate
			continue
		}
		if !commitMark(c, rep, eff, key, content) {
			continue // the store cannot back the marker; leave this message verbatim
		}
		schema.SetMessageText(m, newText)
		changed++
		if key != "" {
			keys = append(keys, key)
		}
	}
	if changed == 0 {
		rep.Skipped = true
	}
	return keys, nil
}

// dedupNote is what the model reads in place of the duplicated output.
//
// It says THIS IS A POINTER and, where the transcript can prove it, WHICH CALL the
// original came from. Both halves are load-bearing, and #281 is what happens without
// them: an agent that had just edited a file, re-ran the same inspection command and
// got a dedup marker back could not tell "your edit changed nothing, here is proof"
// from "this is a caching artifact, unrelated to your edit" — the one moment the
// equivalence claim is being relied on for signal rather than for token savings. It
// spent extra turns re-reading from disk to establish what the marker already knew.
//
// Naming the command is the recoverable half of that. "identical to an earlier tool
// output" asserts an equivalence the model cannot check; "identical to the output of
// `python -c ...` earlier in this conversation" is a claim it can locate, compare
// against its own expectations, and disbelieve — and disbelieving is the point, because
// only the model can initiate recovery via the expand tool. The note therefore states
// WHAT was removed and never why it was safe to remove, the rule coref.go arrived at
// the hard way: wording that reassures talks the model out of the expand call that
// would have repaired a mistake.
//
// WHY NOT THE TURN INDEX, which is what #281 actually suggested. "identical to the
// output at step N" is position-dependent, and dedup's replacement bytes must be a pure
// function of content: it is one of the offloaders Ctx.CacheAware explicitly exempts
// from the cache-tail gate on the grounds that it "is already byte-stable on the
// unchanged prefix" (components/component.go). The transcript grows every turn, so a
// marker carrying the first copy's index would rewrite itself inside the provider's
// cached prefix as messages accumulate, forcing a full-suffix cache write at ~12x the
// read price — paying a large, recurring bill for a reference the model cannot resolve
// anyway (it sees no message indices). The producing command is derived from content
// that is already in the prefix ahead of this message, so it is stable across turns.
// state.go's note on the "xdedup index" reaches the same conclusion from the other side.
func dedupNote(tc schema.ToolCall) string {
	cmd := tc.Command()
	if cmd == "" {
		// No paired tool_use: say only what is true, which is still that this is a pointer.
		return "[identical to an earlier tool output] "
	}
	return "[identical to the output of " + quoteCmd(cmd) + " earlier in this conversation] "
}

// quoteCmd renders a command for the note: backticked, single-line, and length-capped so
// a pathological Write payload cannot turn the pointer into something bigger than the
// output it replaces (tryMark would then gate it as marker_no_win and the duplicate would
// stay verbatim, which is a silent loss of the saving rather than a bug).
func quoteCmd(cmd string) string {
	const max = 80
	cmd = strings.Join(strings.Fields(cmd), " ") // collapse newlines/runs; keep it one line
	if len(cmd) > max {
		// Rune-safe truncation: cutting mid-rune would emit invalid UTF-8 into the request.
		trimmed := strings.ToValidUTF8(cmd[:max], "")
		cmd = trimmed + "…"
	}
	return "`" + cmd + "`"
}

func init() {
	components.RegisterFields("dedup", dedupConfig{}, []components.Field{
		{Key: "min_tokens", Type: components.FieldInt, Default: 100, Min: 1,
			Hint: "Only replace a repeated tool output above this many tokens with a pointer to the first copy."},
		markerModeField(),
	})
}
