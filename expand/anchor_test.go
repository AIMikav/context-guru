package expand

import (
	"strings"
	"testing"

	"github.com/rossoctl/context-guru/store"
	"github.com/tidwall/gjson"
)

// The anchor's fingerprint is taken on the turn the anchor is recorded, when the message it names
// was the LAST one and carried the client's cache_control; on every later turn it does not. Any
// other difference must still be seen.
func TestTheFingerprintIgnoresOnlyCacheControl(t *testing.T) {
	marked := gjson.Parse(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x","cache_control":{"type":"ephemeral"}}]}`)
	plain := gjson.Parse(`{"content":[{"content":"x","tool_use_id":"t1","type":"tool_result"}],"role":"user"}`)
	edited := gjson.Parse(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"y"}]}`)
	if Fingerprint(marked) == "" || Fingerprint(marked) != Fingerprint(plain) {
		t.Fatal("moving cache_control (or reordering keys) changed the fingerprint, so every anchor " +
			"would be dropped on the turn after it was recorded")
	}
	if Fingerprint(plain) == Fingerprint(edited) {
		t.Fatal("a changed message kept its fingerprint, so a rewritten history would still be anchored")
	}
}

func TestAnAnchorIsRecordedOncePerID(t *testing.T) {
	st := store.NewMemory(store.Options{})
	RecordAnchor(st, "s", Anchor{ID: "a", At: 3, Fp: "f"})
	RecordAnchor(st, "s", Anchor{ID: "a", At: 9, Fp: "g"}) // a later expand of the same id
	RecordAnchor(st, "s", Anchor{ID: "b", At: 5, Fp: "h"})
	got := Anchors(st, "s")
	if len(got) != 2 || got[0] != (Anchor{ID: "a", At: 3, Fp: "f"}) || got[1].ID != "b" {
		t.Fatalf("anchors = %+v: the first place content came back must be where it stays", got)
	}
	if len(Anchors(st, "other")) != 0 {
		t.Fatal("anchors leaked across sessions")
	}
}

func TestAnAnchorIsUsableOnlyWhereItWasRecorded(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"go"},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]}]}`)
	at, fp, ok := AnchorPoint("anthropic", body)
	if !ok || at != 2 {
		t.Fatalf("AnchorPoint = %d, %v; want 2 (after the last message)", at, ok)
	}
	a := Anchor{ID: "x", At: at, Fp: fp}
	next := gjson.Parse(`[{"role":"user","content":"go"},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"done"}]},{"role":"user","content":"next"}]`).Array()
	if !a.Usable("anthropic", next) {
		t.Fatal("the anchor is not usable on the very next turn")
	}
	compacted := gjson.Parse(`[{"role":"user","content":"summary of everything"},{"role":"user","content":"next"}]`).Array()
	if a.Usable("anthropic", compacted) {
		t.Fatal("the anchor survived the client compacting its own history")
	}
	split := gjson.Parse(`[{"role":"user","content":"go"},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},` +
		`{"role":"tool","tool_call_id":"c","content":"r"}]`).Array()
	if a.Usable("openai", split) {
		t.Fatal("an anchor that would split a tool answer from its call was used")
	}
	if _, _, ok := AnchorPoint("anthropic", []byte(`{"messages":[{"role":"assistant","content":"prefill"}]}`)); ok {
		t.Fatal("an anchor after an assistant prefill was offered")
	}
	if _, _, ok := AnchorPoint("responses", []byte(`{"previous_response_id":"r","input":[{"role":"user","content":"x"}]}`)); ok {
		t.Fatal("an anchor was stated in a Responses delta whose history the server holds")
	}
}

// Inserting must leave every other message's bytes exactly as they were — the whole point is
// that nothing before the anchor changes.
func TestInsertRestoredKeepsEveryOtherMessageByteForByte(t *testing.T) {
	for _, wire := range []string{"anthropic", "openai", "responses"} {
		t.Run(wire, func(t *testing.T) {
			field := MessagesField(wire)
			body := []byte(`{"model":"m","` + field + `":[{"role":"user","content":"a"},{"role":"user", "content" : "b"},{"role":"assistant","content":"c"}]}`)
			out, ok := InsertRestored(wire, body, []Insertion{{At: 2, Text: RestoredText("abc", "ORIG")}})
			if !ok {
				t.Fatal("insert declined")
			}
			before, after := gjson.GetBytes(body, field).Array(), gjson.GetBytes(out, field).Array()
			if len(after) != 4 || after[0].Raw != before[0].Raw || after[1].Raw != before[1].Raw || after[3].Raw != before[2].Raw {
				t.Fatalf("neighbours changed:\n%s", out)
			}
			ins := after[2]
			if ins.Get("role").String() != "user" || !strings.Contains(ins.Raw, "ORIG") ||
				!strings.Contains(gjson.Get(ins.Raw, "@this").String(), "abc") {
				t.Fatalf("restored message malformed: %s", ins.Raw)
			}
			text := ins.Get("content").String()
			if wire != "openai" {
				text = ins.Get("content.0.text").String()
			}
			if !strings.Contains(text, Marker("abc")) || !strings.HasSuffix(text, "ORIG") {
				t.Fatalf("restored text %q does not carry the marker and the original", text)
			}
			if wire == "responses" && (ins.Get("type").String() != "message" || ins.Get("content.0.type").String() != "input_text") {
				t.Fatalf("not a Responses input message: %s", ins.Raw)
			}
		})
	}
}

func TestCallSitesFindTheTurnThatExpanded(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu","name":"` + ToolName + `","input":{"id":"H"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu","content":"note"}]}]}`)
	cs := CallSites("anthropic", body)
	if len(cs) != 1 || cs[0] != (CallSite{ID: "H", At: 1, Answer: "note"}) {
		t.Fatalf("call sites = %+v", cs)
	}
	oa := []byte(`{"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"` + ToolName + `","arguments":"{\"id\":\"H\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"note"}]}`)
	if cs := CallSites("openai", oa); len(cs) != 1 || cs[0].At != 1 || cs[0].ID != "H" {
		t.Fatalf("openai call sites = %+v", cs)
	}
	rs := []byte(`{"input":[{"role":"user","content":"go"},` +
		`{"type":"function_call","call_id":"c1","name":"` + ToolName + `","arguments":"{\"id\":\"H\"}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"note"}]}`)
	if cs := CallSites("responses", rs); len(cs) != 1 || cs[0].At != 1 || cs[0].Answer != "note" {
		t.Fatalf("responses call sites = %+v", cs)
	}
}

// JSON numbers must keep their exact value. Decoded as float64, two integers above 2^53 that
// differ by 1 become the same number, so two different messages would share a fingerprint.
func TestTheFingerprintKeepsLargeIntegersExact(t *testing.T) {
	a := gjson.Parse(`{"role":"user","content":[{"type":"text","text":"x","n":9007199254740993}]}`)
	b := gjson.Parse(`{"role":"user","content":[{"type":"text","text":"x","n":9007199254740992}]}`)
	if Fingerprint(a) == "" || Fingerprint(a) == Fingerprint(b) {
		t.Fatal("two messages that differ only in an integer above 2^53 got the same fingerprint")
	}
}
