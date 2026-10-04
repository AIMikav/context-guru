package cheapmodel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenAIPrefixedResponsesPreservesSentPrefix(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong prefixed request: path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"keep\":[]}"}]}],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1100,"cache_write_tokens":0},"output_tokens":20}}`))
	}))
	defer up.Close()
	body := []byte(`{"model":"gpt-5.6","stream":true,"input":[{"role":"user","content":"task"},{"type":"reasoning","encrypted_content":"opaque"}]}`)
	cli := OpenAI{BaseURL: up.URL, Model: "gpt-5.6", APIKey: "test-key", MaxTokens: 100}
	reply, usage, err := cli.CompletePrefixedResponses(context.Background(), body, "judge")
	if err != nil || reply != `{"keep":[]}` || usage.CacheRead != 1100 || usage.Fresh != 100 || usage.Output != 20 {
		t.Fatalf("reply=%q usage=%+v err=%v", reply, usage, err)
	}
	if gjson.GetBytes(got, "input.1.encrypted_content").String() != "opaque" ||
		gjson.GetBytes(got, "input.2.content").String() != "judge" ||
		gjson.GetBytes(got, "stream").Exists() || gjson.GetBytes(got, "max_output_tokens").Int() != 100 {
		t.Fatalf("prefix ask changed the sent history: %s", got)
	}
}

func TestOpenAIPrefixedResponsesReadsAdjudicationTool(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"function_call","name":"context_guru_adjudicate",` +
			`"arguments":"{\"verdicts\":[{\"i\":0,\"verdict\":\"keep\"}]}"}],` +
			`"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1100},"output_tokens":20}}`))
	}))
	defer up.Close()
	cli := OpenAI{BaseURL: up.URL, Model: "gpt-5.6", APIKey: "test-key"}
	reply, usage, err := cli.CompletePrefixedResponses(context.Background(),
		[]byte(`{"model":"gpt-5.6","input":[{"role":"user","content":"task"}]}`), "judge")
	if err != nil || reply != `{"verdicts":[{"i":0,"verdict":"keep"}]}` || !usage.ViaTool {
		t.Fatalf("reply=%q usage=%+v err=%v", reply, usage, err)
	}
}
