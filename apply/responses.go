package apply

// Native OpenAI Responses request adaptation. Responses input is deliberately
// not converted to a Chat Completions envelope: compatible items get a temporary
// component view and changed text is spliced back into its original wire field.

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/modes"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/session"
	"github.com/rossoctl/context-guru/store"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responseSlot struct {
	path string
	text string
	pre  []byte
}

// A count-changing rewrite can only be serialized when every input item has
// exactly one text slot. Reasoning, calls, images and multi-block items carry
// state that must not be dropped or moved by a summary.
func responsesCountChangeSafe(body []byte, slots []responseSlot) bool {
	in := gjson.GetBytes(body, "input")
	if !in.IsArray() {
		return false
	}
	want := len(in.Array())
	if gjson.GetBytes(body, "instructions").Type == gjson.String {
		want++
	}
	if len(slots) != want {
		return false
	}
	for i, item := range in.Array() {
		if typ := item.Get("type").String(); typ != "" && typ != "message" {
			return false
		}
		if role := item.Get("role").String(); role != "user" && role != "assistant" && role != "system" && role != "developer" {
			return false
		}
		base := "input." + strconv.Itoa(i) + ".content"
		content := item.Get("content")
		if content.Type == gjson.String {
			if !hasResponseSlot(slots, base) {
				return false
			}
			continue
		}
		if !content.IsArray() || len(content.Array()) != 1 {
			return false
		}
		block := content.Array()[0]
		if kind := block.Get("type").String(); kind != "input_text" && kind != "output_text" {
			return false
		}
		if block.Get("text").Type != gjson.String || !hasResponseSlot(slots, base+".0.text") {
			return false
		}
	}
	return true
}

func hasResponseSlot(slots []responseSlot, path string) bool {
	for _, slot := range slots {
		if slot.path == path {
			return true
		}
	}
	return false
}

// Rebuild a plain-text Responses input around the one newly inserted summary.
// Survivors keep their original wire shape, including any in-place text edits
// made by later components. Any ambiguous mapping fails open.
func rebuildResponsesCountChanged(body []byte, norm []bschemas.ChatMessage, slots []responseSlot, out []bschemas.ChatMessage) ([]byte, bool) {
	start := 0
	if len(slots) > 0 && slots[0].path == "instructions" {
		if len(out) == 0 {
			return body, false
		}
		b, _ := json.Marshal(out[0])
		if !bytes.Equal(b, slots[0].pre) {
			return body, false
		}
		start = 1
	}
	if len(out) >= len(norm) || len(out) <= start {
		return body, false
	}
	// Only summarize currently changes count, and its wrapper identifies the
	// inserted message without relying on retained text staying unchanged.
	head := -1
	for i := start; i < len(out); i++ {
		text := schema.MessageText(out[i])
		if strings.HasPrefix(text, "=== History Summary ===") && strings.Contains(text, "<<cg:") {
			if head >= 0 {
				return body, false
			}
			head = i
		}
	}
	if head < start || out[head].Role != bschemas.ChatMessageRoleUser {
		return body, false
	}
	tail := len(out) - head - 1
	if head >= len(slots)-tail || head < start {
		return body, false
	}
	items := gjson.GetBytes(body, "input").Array()
	var parts [][]byte
	appendSurvivor := func(outIdx, origIdx int) bool {
		if out[outIdx].Role != norm[origIdx].Role {
			return false
		}
		itemIdx := origIdx - start
		if itemIdx < 0 || itemIdx >= len(items) {
			return false
		}
		item := []byte(items[itemIdx].Raw)
		if now := schema.MessageText(out[outIdx]); now != slots[origIdx].text {
			pathParts := strings.SplitN(slots[origIdx].path, ".", 3)
			if len(pathParts) != 3 {
				return false
			}
			var err error
			item, err = sjson.SetBytes(item, pathParts[2], now)
			if err != nil {
				return false
			}
		}
		parts = append(parts, item)
		return true
	}
	for i := start; i < head; i++ {
		if !appendSurvivor(i, i) {
			return body, false
		}
	}
	summary, err := json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{"user", schema.MessageText(out[head])})
	if err != nil {
		return body, false
	}
	parts = append(parts, summary)
	for i := 0; i < tail; i++ {
		if !appendSurvivor(head+1+i, len(norm)-tail+i) {
			return body, false
		}
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, part := range parts {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(part)
	}
	buf.WriteByte(']')
	next, err := sjson.SetRawBytes(body, "input", buf.Bytes())
	return next, err == nil
}

func responseMessage(role string, text string) bschemas.ChatMessage {
	r := bschemas.ChatMessageRoleUser
	switch role {
	case "system", "developer":
		r = bschemas.ChatMessageRoleSystem
	case "assistant":
		r = bschemas.ChatMessageRoleAssistant
	case "tool":
		r = bschemas.ChatMessageRoleTool
	}
	m := bschemas.ChatMessage{Role: r}
	schema.SetMessageText(&m, text)
	return m
}

func normalizeResponses(body []byte) (out []bschemas.ChatMessage, slots []responseSlot) {
	if v := gjson.GetBytes(body, "instructions"); v.Type == gjson.String {
		m := responseMessage("system", v.String())
		pre, _ := json.Marshal(m)
		out, slots = append(out, m), append(slots, responseSlot{"instructions", v.String(), pre})
	}
	in := gjson.GetBytes(body, "input")
	if in.Type == gjson.String {
		m := responseMessage("user", in.String())
		pre, _ := json.Marshal(m)
		return append(out, m), append(slots, responseSlot{"input", in.String(), pre})
	}
	if !in.IsArray() {
		return out, slots
	}
	for i, item := range in.Array() {
		base := "input." + strconv.Itoa(i)
		typ := item.Get("type").String()
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			output := item.Get("output")
			add := func(text, path string) {
				m := toolMessage(text, item.Get("call_id").String())
				pre, _ := json.Marshal(m)
				out, slots = append(out, m), append(slots, responseSlot{path, text, pre})
			}
			if output.Type == gjson.String {
				add(output.String(), base+".output")
			} else if output.IsArray() {
				for j, block := range output.Array() {
					if (block.Get("type").String() == "input_text" || block.Get("type").String() == "output_text") && block.Get("text").Type == gjson.String {
						add(block.Get("text").String(), base+".output."+strconv.Itoa(j)+".text")
					}
				}
			}
			continue
		}
		if typ != "message" && item.Get("role").String() == "" {
			continue // reasoning, function_call, item_reference: preserve, never reinterpret
		}
		role := item.Get("role").String()
		content := item.Get("content")
		if content.Type == gjson.String {
			m := responseMessage(role, content.String())
			pre, _ := json.Marshal(m)
			out, slots = append(out, m), append(slots, responseSlot{base + ".content", content.String(), pre})
			continue
		}
		if !content.IsArray() {
			continue
		}
		for j, block := range content.Array() {
			kind := block.Get("type").String()
			if (kind != "input_text" && kind != "output_text") || block.Get("text").Type != gjson.String {
				continue
			}
			m := responseMessage(role, block.Get("text").String())
			pre, _ := json.Marshal(m)
			out, slots = append(out, m), append(slots, responseSlot{base + ".content." + strconv.Itoa(j) + ".text", block.Get("text").String(), pre})
		}
	}
	return out, slots
}

func bodyResponsesOpts(ctx context.Context, pipe *components.Pipeline, st store.Store, o Opts) Result {
	res := Result{Body: o.Body}
	res.Bypassed = o.Bypass
	norm, slots := normalizeResponses(o.Body)
	res.Messages = len(norm)
	if len(norm) == 0 {
		return res
	}
	sys, first := schema.SessionHead(norm)
	res.Session = session.Scoped(o.Tenant, explicitSession(o.Session, o.Body), sys, first)
	cacheAware := resolveCacheAware(o.CacheMode, bschemas.OpenAI, o.Body)
	maxCachedIdx, idleMs := -1, int64(-1)
	nowMs := o.nowMs()
	if cacheAware {
		if o.Tracker != nil {
			var prevAt int64
			maxCachedIdx, prevAt = o.Tracker.TurnAt(res.Session, len(norm), nowMs)
			maxCachedIdx--
			alias := session.Scoped(o.Tenant, "", sys, first)
			if at := aliasSeen(st, alias, nowMs); at > prevAt {
				prevAt = at
			}
			if prevAt > 0 && nowMs >= prevAt {
				idleMs = nowMs - prevAt
			}
		} else {
			maxCachedIdx = modes.Boundary(prevLen(st, res.Session), len(norm)) - 1
			defer putLen(st, res.Session, len(norm))
		}
	}
	res.CacheAware, res.MaxCachedIdx = cacheAware, maxCachedIdx
	if o.Bypass || pipe == nil {
		return res
	}
	mode := o.Mode
	if mode == "" {
		mode = components.ModeSync
	}
	ttl, ttlKind := CacheLifetime(bschemas.OpenAI, gjson.GetBytes(o.Body, "model").String(), o.Body)
	if !cacheAware {
		ttl, ttlKind = 0, CacheLifetimeUnknown
	}
	chat := &bschemas.BifrostChatRequest{Provider: bschemas.OpenAI, Input: append([]bschemas.ChatMessage(nil), norm...)}
	c := &components.Ctx{Ctx: ctx, Session: res.Session, Store: st, Model: o.Models,
		DisallowCountChange: !responsesCountChangeSafe(o.Body, slots),
		CtxWindow:           o.Window, CtxWindowExact: o.WindowExact, CompactionPoint: o.CompactionPoint,
		CompactionPointSource: o.CompactionPointSource, ModelName: gjson.GetBytes(o.Body, "model").String(),
		Mode: mode, CacheAware: cacheAware, MaxCachedIdx: maxCachedIdx, IdleMs: idleMs,
		CacheTTLMs: ttl.Milliseconds(), CacheTTLMinimum: ttlKind == CacheLifetimeMinimum,
		PrevBilledInput: prevBilledInput(st, res.Session), SelfRates: o.SelfRates, RatesFor: o.RatesFor}
	res.Run = pipe.Run(chat, c)
	start := 0
	if cacheAware && maxCachedIdx >= 0 {
		start = min(maxCachedIdx+1, len(norm))
	}
	res.AttemptedTokens = schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: norm[start:]})
	res.FrozenTokens = schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: norm[:start]})
	if len(chat.Input) != len(norm) {
		if next, ok := rebuildResponsesCountChanged(o.Body, norm, slots, chat.Input); ok {
			res.Body, res.Changed = next, true
		}
		return res
	}
	body := o.Body
	for i := range chat.Input {
		now, err := json.Marshal(chat.Input[i])
		if err != nil || bytes.Equal(now, slots[i].pre) {
			continue
		}
		text := schema.MessageText(chat.Input[i])
		if text == slots[i].text {
			continue
		}
		next, err := sjson.SetBytes(body, slots[i].path, text)
		if err != nil {
			return Result{Body: o.Body, Trace: res.Trace}
		}
		body, res.Changed = next, true
	}
	res.Body = body
	return res
}
