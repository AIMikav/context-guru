package apply

import (
	"strings"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/schema"
)

// A one-message span replaced by summarize keeps the same count but changes
// the wire structure. Both adapters must rebuild instead of editing only text.
func summaryStructureChanged(before, after []bschemas.ChatMessage) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range after {
		text := schema.MessageText(after[i])
		if strings.HasPrefix(text, "=== History Summary ===") && text != schema.MessageText(before[i]) {
			return true
		}
	}
	return false
}
