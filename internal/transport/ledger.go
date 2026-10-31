package transport

import (
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// ledgerData adds the token-ledger fields to a tool event's data: bytes and
// tokens for what the tool produced (raw) and what went back to the agent
// (returned), plus the return mode that was asked for. Tokens are recorded
// at event time with sandbox.ApproxTokens because bytes alone misjudge CJK
// text; the byte fields stay for journals and readers that predate tokens.
func ledgerData(data map[string]any, mode, raw string, returned ...string) map[string]any {
	if mode == "" {
		mode = "auto"
	}
	retBytes, retTokens := 0, 0
	for _, s := range returned {
		retBytes += len(s)
		retTokens += sandbox.ApproxTokens(s)
	}
	data[core.KeyRawBytes] = len(raw)
	data[core.KeyReturnedBytes] = retBytes
	data[core.KeyRawTokens] = sandbox.ApproxTokens(raw)
	data[core.KeyReturnedTokens] = retTokens
	data[core.KeyReturnMode] = mode
	return data
}

// sizeTally accumulates the bytes and tokens of a response that is returned
// as produced (grep, glob), where raw == returned by definition.
type sizeTally struct{ bytes, tokens int }

func (t *sizeTally) add(s string) {
	t.bytes += len(s)
	t.tokens += sandbox.ApproxTokens(s)
}

// apply records the tally as both raw and returned: the call is visible in
// the ledger and claims no saving.
func (t *sizeTally) apply(data map[string]any) map[string]any {
	data[core.KeyRawBytes] = t.bytes
	data[core.KeyReturnedBytes] = t.bytes
	data[core.KeyRawTokens] = t.tokens
	data[core.KeyReturnedTokens] = t.tokens
	return data
}

// matchTexts lists the excerpt bodies of a response, for ledgerData.
func matchTexts(ms []sandbox.ContentMatch) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}
