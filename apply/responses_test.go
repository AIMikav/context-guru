package apply_test

import (
	"context"
	"strings"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/apply"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
	"github.com/tidwall/gjson"
)

type responseReducer struct{}

func (responseReducer) Name() string                 { return "responses-test" }
func (responseReducer) Enabled(*components.Ctx) bool { return true }
func (responseReducer) Reformat(req *bschemas.BifrostChatRequest, _ *components.Report, _ *components.Ctx) error {
	for i := range req.Input {
		if req.Input[i].Role == bschemas.ChatMessageRoleTool {
			schema.SetMessageText(&req.Input[i], "reduced")
		}
	}
	return nil
}

func TestResponsesRewritesToolOutputWithoutChangingEnvelope(t *testing.T) {
	body := []byte(` { "model":"gpt-5", "instructions":"keep me", "input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},` +
		`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},` +
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"` + strings.Repeat("large output ", 20) + `"}` +
		`], "stream":true } `)
	p := components.NewPipeline([]components.Component{responseReducer{}}, nil)
	res := apply.BodyOpts(context.Background(), p, store.NewMemory(store.Options{}), apply.Opts{
		Provider: bschemas.OpenAI, API: "responses", Body: body,
	})
	if !res.Changed {
		t.Fatal("expected compatible tool output to be rewritten")
	}
	if got := gjson.GetBytes(res.Body, "input.3.output").String(); got != "reduced" {
		t.Fatalf("output = %q", got)
	}
	for _, path := range []string{"model", "instructions", "input.0", "input.1", "input.2", "stream"} {
		if a, b := gjson.GetBytes(res.Body, path).Raw, gjson.GetBytes(body, path).Raw; a != b {
			t.Errorf("untouched %s changed:\nwant %s\n got %s", path, b, a)
		}
	}
}

func TestResponsesRewritesCodexCustomToolOutputBlocks(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[` +
		`{"type":"custom_tool_call","call_id":"call_1","name":"exec_command","input":"{}"},` +
		`{"type":"custom_tool_call_output","call_id":"call_1","output":[` +
		`{"type":"input_text","text":"metadata"},` +
		`{"type":"input_text","text":"` + strings.Repeat("large output ", 20) + `"},` +
		`{"type":"input_image","image_url":"opaque"}]}` +
		`]}`)
	p := components.NewPipeline([]components.Component{responseReducer{}}, nil)
	res := apply.BodyOpts(context.Background(), p, store.NewMemory(store.Options{}), apply.Opts{
		Provider: bschemas.OpenAI, API: "responses", Body: body,
	})
	if !res.Changed {
		t.Fatal("expected custom tool output blocks to be rewritten")
	}
	for _, path := range []string{"input.1.output.0.text", "input.1.output.1.text"} {
		if got := gjson.GetBytes(res.Body, path).String(); got != "reduced" {
			t.Errorf("%s = %q", path, got)
		}
	}
	for _, path := range []string{"input.0", "input.1.output.2"} {
		if a, b := gjson.GetBytes(res.Body, path).Raw, gjson.GetBytes(body, path).Raw; a != b {
			t.Errorf("untouched %s changed:\nwant %s\n got %s", path, b, a)
		}
	}
}

func TestResponsesUnsupportedInputPassesThroughByteForByte(t *testing.T) {
	body := []byte(" { \"model\": \"gpt-5\", \"input\": [{\"type\":\"reasoning\",\"id\":\"r\"}] } \n")
	p := components.NewPipeline([]components.Component{responseReducer{}}, nil)
	res := apply.BodyOpts(context.Background(), p, store.NewMemory(store.Options{}), apply.Opts{
		Provider: bschemas.OpenAI, API: "responses", Body: body,
	})
	if res.Changed || string(res.Body) != string(body) {
		t.Fatalf("passthrough changed bytes:\nwant %q\n got %q", body, res.Body)
	}
}
