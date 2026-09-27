package offload

import (
	"strings"
	"testing"
	"unicode/utf8"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

func dedupFor(t *testing.T) *Dedup {
	t.Helper()
	comp, err := newDedup([]byte("min_tokens: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	return comp.(*Dedup)
}

// runDedup offloads msgs and returns the rewritten text of each message, plus the report.
func runDedup(t *testing.T, d *Dedup, msgs []bschemas.ChatMessage) ([]string, *components.Report) {
	t.Helper()
	req := &bschemas.BifrostChatRequest{Input: msgs}
	c := &components.Ctx{Session: "s", Store: store.NewMemory(store.Options{})}
	var rep components.Report
	if _, err := d.Offload(req, &rep, c); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(req.Input))
	for i := range req.Input {
		out[i] = schema.MessageText(req.Input[i])
	}
	return out, &rep
}

// #281: an agent that edits a file and re-runs the inspection command gets a dedup
// marker back, and from the marker alone could not tell "your edit changed nothing,
// here is proof" from "this is a caching artifact". The marker must therefore carry
// BOTH halves of what makes that decidable: that it is a pointer at all, and how to
// get the bytes it points at. The recovery instruction is the half that was missing —
// dedup was the only offloader of eleven that passed an empty hint to tryMark, so its
// duplicates went out as a bare <<cg:HASH>> with nothing saying the content was
// retrievable, while every sibling said "[full output: call context_guru_expand]".
func TestTheDedupMarkerSaysHowToGetTheOutputBack(t *testing.T) {
	dump := strings.Repeat("verbose tool output line\n", 30)
	msgs := []bschemas.ChatMessage{tool(dump), tool(dump)}
	got, _ := runDedup(t, dedupFor(t), msgs)

	if got[0] != dump {
		t.Fatal("the first copy must be left verbatim; only later duplicates collapse")
	}
	dup := got[1]
	if !strings.Contains(dup, expand.ToolName) {
		t.Fatalf("the marker does not name the expand tool, so the model is told the output "+
			"is a duplicate but not that it can recover it — the #281 trust gap: %q", dup)
	}
	if !strings.Contains(dup, "<<cg:") {
		t.Fatalf("no resolvable marker, so there is nothing to recover: %q", dup)
	}
}

// The other half of #281: WHICH earlier output. "identical to an earlier tool output"
// asserts an equivalence the model cannot locate or check. Naming the producing command
// makes it checkable — and disbelievable, which is the point, since only the model can
// initiate recovery.
func TestTheDedupMarkerNamesTheCallTheOutputCameFrom(t *testing.T) {
	dump := strings.Repeat("parsed 41 records\n", 30)
	cmd := `python -c "import extract_team; print(extract_team.parse())"`
	var msgs []bschemas.ChatMessage
	msgs = callMsgs(msgs, "Bash", map[string]string{"command": cmd}, dump)
	msgs = callMsgs(msgs, "Bash", map[string]string{"command": cmd}, dump)

	got, _ := runDedup(t, dedupFor(t), msgs)
	dup := got[3] // assistant, tool, assistant, tool
	if !strings.Contains(dup, "python -c") {
		t.Fatalf("the marker does not name the command that produced the original, so the "+
			"model cannot tell which earlier output this claims to equal: %q", dup)
	}
}

// A tool result with no paired tool_use still has to say it is a pointer. The note
// degrades to the unqualified wording rather than inventing a source or emitting an
// empty "identical to the output of “".
func TestTheDedupMarkerStillIdentifiesItselfWithNoPairedCall(t *testing.T) {
	dump := strings.Repeat("verbose tool output line\n", 30)
	got, _ := runDedup(t, dedupFor(t), []bschemas.ChatMessage{tool(dump), tool(dump)})
	dup := got[1]
	if !strings.Contains(dup, "identical to an earlier tool output") {
		t.Fatalf("an unpaired duplicate must still announce itself as a pointer: %q", dup)
	}
	if strings.Contains(dup, "``") {
		t.Fatalf("empty command rendered as backticks: %q", dup)
	}
}

// The note goes inside the request, so a pathological command must not make the pointer
// unbounded (or invalid UTF-8, which a naive byte slice through a multibyte rune emits).
func TestTheDedupNoteBoundsAndSanitizesTheCommand(t *testing.T) {
	for _, tc := range []struct{ name, cmd string }{
		{"long ascii", strings.Repeat("x", 4000)},
		{"multibyte straddling the cut", strings.Repeat("a", 79) + strings.Repeat("日", 40)},
		{"embedded newlines", "line one\nline two\nline three"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := dedupNote(schema.ToolCall{Name: "Bash", Args: `{"command":` + jsonQuote(tc.cmd) + `}`})
			if !utf8.ValidString(note) {
				t.Fatalf("note is not valid UTF-8, which corrupts the outgoing request: %q", note)
			}
			if strings.ContainsAny(note, "\n\r") {
				t.Fatalf("a multi-line note breaks the one-line pointer shape: %q", note)
			}
			if len(note) > 200 {
				t.Fatalf("note is %d bytes; an unbounded pointer can cost more than the "+
					"output it replaces", len(note))
			}
		})
	}
}

// The marker's bytes must be a pure function of content: dedup is one of the offloaders
// Ctx.CacheAware exempts from the cache-tail gate on the grounds that it is "already
// byte-stable on the unchanged prefix". This is why #281's own suggestion — annotate the
// marker with the turn index it matches — is NOT what this fix implements: the index
// shifts as the transcript grows, so the same content would render different bytes inside
// the provider's cached prefix on a later turn and force a full-suffix cache write.
// Re-running with the pair pushed deeper must produce the identical replacement.
func TestTheDedupMarkerIsByteStableAsTheTranscriptGrows(t *testing.T) {
	dump := strings.Repeat("parsed 41 records\n", 30)
	cmd := "pytest -q"
	build := func(lead int) []bschemas.ChatMessage {
		var msgs []bschemas.ChatMessage
		for i := 0; i < lead; i++ {
			msgs = append(msgs, bschemas.ChatMessage{Role: bschemas.ChatMessageRoleUser,
				Content: &bschemas.ChatMessageContent{ContentStr: ptr("turn " + strings.Repeat("x", i))}})
		}
		msgs = callMsgs(msgs, "Bash", map[string]string{"command": cmd}, dump)
		return callMsgs(msgs, "Bash", map[string]string{"command": cmd}, dump)
	}

	shallow, _ := runDedup(t, dedupFor(t), build(0))
	deep, _ := runDedup(t, dedupFor(t), build(5))
	if shallow[len(shallow)-1] != deep[len(deep)-1] {
		t.Fatalf("the replacement moved with position:\n shallow %q\n deep    %q\n"+
			"a position-dependent marker rewrites itself inside the provider's cached prefix "+
			"as the transcript grows, forcing a full-suffix cache write", shallow[len(shallow)-1], deep[len(deep)-1])
	}
}

func ptr(s string) *string { return &s }
