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

// matchTexts lists the excerpt bodies of a response, for ledgerData.
func matchTexts(ms []sandbox.ContentMatch) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}
