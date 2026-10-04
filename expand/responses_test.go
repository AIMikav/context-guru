package expand

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesExpandWireAdapter(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","input":[{"role":"user","content":"find <<cg:HASH>>"}],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`)
	injected, ok := Inject("responses", InjectAuto, body, true)
	if !ok || !HasTool("responses", injected) || gjson.GetBytes(injected, "tools.1.name").String() != ToolName ||
		gjson.GetBytes(injected, "tools.1.parameters.type").String() != "object" {
		t.Fatalf("Responses tool was not injected in native shape: %s", injected)
	}
	if again, ok := Inject("responses", InjectAuto, injected, true); ok || string(again) != string(injected) {
		t.Fatal("Responses injection was not idempotent")
	}
	resp := []byte(`{"output":[{"type":"reasoning","encrypted_content":"opaque"},` +
		`{"type":"function_call","call_id":"c1","name":"context_guru_expand","arguments":"{\"id\":\"HASH\"}"}]}`)
	calls, other := ResponseCalls("responses", resp)
	if other || len(calls) != 1 || calls[0].CallID != "c1" || calls[0].HashID != "HASH" {
		t.Fatalf("Responses calls = %+v other=%v", calls, other)
	}
	next, ok := Continuation("responses", injected, resp, map[string]string{"c1": "original text"})
	if !ok || gjson.GetBytes(next, "input.1.encrypted_content").String() != "opaque" ||
		gjson.GetBytes(next, "input.3.type").String() != "function_call_output" ||
		gjson.GetBytes(next, "input.3.output").String() != "original text" {
		t.Fatalf("Responses continuation lost state or result: %s", next)
	}
}

func TestResponsesSSEAndRequestRepair(t *testing.T) {
	stream := []byte("event: response.completed\n" +
		`data: {"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"c1","name":"context_guru_expand","arguments":"{\"id\":\"HASH\"}"}]}}` + "\n\n")
	resp, ok := AggregateSSE("responses", stream)
	if !ok || gjson.GetBytes(resp, "output.0.call_id").String() != "c1" {
		t.Fatalf("Responses SSE terminal event was not recovered: %s", resp)
	}
	body := []byte(`{"input":[{"type":"function_call","call_id":"c1","name":"context_guru_expand","arguments":"{\"id\":\"HASH\"}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"No such tool available"}]}`)
	got, restored := RepairToolResults("responses", body, func(id string) (string, bool) {
		return "original text", id == "HASH"
	})
	if len(restored) != 1 || restored[0] != "original text" || gjson.GetBytes(got, "input.1.output").String() != "original text" {
		t.Fatalf("Responses repair failed: %s restored=%v", got, restored)
	}
	if !HasMarkersInMessages([]byte(`{"input":[{"role":"user","content":"` + Marker("HASH") + `"}]}`)) {
		t.Fatal("Responses marker was not found")
	}
	if strings.Contains(string(ToolDefRaw("responses")), `"function":`) {
		t.Fatal("Responses definition used Chat Completions shape")
	}
}

func TestResponsesSummaryExpandRestoresImageAsImage(t *testing.T) {
	req := []byte(`{"input":[{"role":"user","content":"=== History Summary === old image <<cg:HASH>>"},` +
		`{"role":"user","content":"what was in it?"}]}`)
	resp := []byte(`{"output":[{"type":"function_call","call_id":"c1","name":"context_guru_expand","arguments":"{\"id\":\"HASH\"}"}]}`)
	stash := `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},` +
		`{"role":"assistant","content":"I saw a diagram"}]`
	next, ok := Continuation("responses", req, resp, map[string]string{"c1": stash})
	if !ok || gjson.GetBytes(next, "input.0.content.0.type").String() != "input_image" ||
		gjson.GetBytes(next, "input.1.content").String() != "I saw a diagram" ||
		gjson.GetBytes(next, "input.2.content").String() != "what was in it?" ||
		gjson.GetBytes(next, "input.4.output").String() != "[expand: original history restored in place above]" {
		t.Fatalf("multimodal original was not restored in its native place: %s", next)
	}
}
