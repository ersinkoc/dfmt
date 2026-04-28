package sandbox

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TailBytes is the size of the tail snippet we keep on the auto path when no
// keyword matches landed. Test/build/CI tools put the verdict at the bottom;
// dropping the entire body in that case turned a passing test run into a
// blank "(no matches)" report. 2 KB is enough for the last ~30-50 log lines
// without re-introducing a meaningful chunk of the original payload.
const TailBytes = 2 * 1024

// rleMinReps is the minimum number of consecutive duplicate lines before RLE
// compaction kicks in. 1-3 reps aren't worth a "(repeated N times)" annotation
// — the saving is smaller than the label.
const rleMinReps = 4

// ansiCSI matches CSI escape sequences (cursor moves, color, style). The
// terminating byte is in the range 0x40-0x7E; for our purposes a letter is
// the practical match. We never expand or interpret these — they're noise
// in tool output captured for an LLM.
var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// ansiOSC matches OSC sequences (set window title, hyperlinks). Terminator is
// BEL (0x07) or ESC \\ (ST). The bracket reference set is conservative — we
// stop at the first terminator-class byte so a runaway sequence doesn't eat
// the rest of the output.
var ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// englishStopwords for intent parsing. Combined with core.TurkishStopwords
// via isStopword so vocabulary/keyword extraction stays clean on multilingual
// content — without the merge, Turkish "ile"/"için"/"olan" would dominate
// the vocabulary list for any Turkish input.
var englishStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "has": {}, "he": {}, "in": {}, "is": {}, "it": {},
	"its": {}, "of": {}, "on": {}, "that": {}, "the": {}, "to": {}, "was": {},
	"were": {}, "will": {}, "with": {}, "i": {}, "want": {}, "find": {}, "show": {},
	"get": {}, "list": {}, "look": {}, "see": {}, "need": {}, "this": {}, "these": {},
	"those": {}, "my": {}, "me": {}, "we": {}, "our": {}, "us": {}, "you": {},
	"your": {}, "have": {}, "had": {}, "do": {}, "does": {}, "did": {}, "can": {},
	"could": {}, "would": {}, "should": {}, "may": {}, "might": {}, "must": {},
	"shall": {}, "am": {}, "been": {}, "being": {},
}

// isStopword folds the English set above with core.TurkishStopwords so a
// single lookup covers both languages. The merge is at-call-site rather than
// at-init-time to avoid a one-time copy and keep the two source tables
// independently maintainable.
func isStopword(tok string) bool {
	if _, ok := englishStopwords[tok]; ok {
		return true
	}
	if _, ok := core.TurkishStopwords[tok]; ok {
		return true
	}
	return false
}

// ExtractKeywords parses an intent string into searchable keywords.
func ExtractKeywords(intent string) []string {
	if intent == "" {
		return nil
	}

	var tokens []string
	var current strings.Builder

	for _, r := range strings.ToLower(intent) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 2 {
				tok := current.String()
				if !isStopword(tok) {
					tokens = append(tokens, tok)
				}
			}
			current.Reset()
		}
	}

	if current.Len() >= 2 {
		tok := current.String()
		if !isStopword(tok) {
			tokens = append(tokens, tok)
		}
	}

	// Deduplicate while preserving order
	seen := make(map[string]struct{}, len(tokens))
	unique := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			unique = append(unique, t)
		}
	}

	return unique
}

// Excerpt represents a scored text excerpt.
type Excerpt struct {
	Text  string
	Score float64
	Line  int
}

// MatchContent scores lines against keywords and returns top matches.
func MatchContent(content string, keywords []string, maxMatches int) []ContentMatch {
	if len(keywords) == 0 || content == "" {
		return nil
	}

	lines := strings.Split(content, "\n")
	excerpts := make([]Excerpt, 0, len(lines))

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		score := scoreLine(line, keywords)
		if score > 0 {
			// Long log lines (200+ chars) are penalised in scoring but were
			// still serialised in full — paying ~250 bytes per match for
			// content the agent rarely needs past the first ~120 chars.
			// truncate is rune-aligned, so non-ASCII (Turkish, CJK) lines
			// don't get cut mid-rune.
			excerpts = append(excerpts, Excerpt{
				Text:  truncate(line, 120),
				Score: score,
				Line:  i + 1,
			})
		}
	}

	// Sort by score descending
	sort.Slice(excerpts, func(i, j int) bool {
		return excerpts[i].Score > excerpts[j].Score
	})

	// Take top N
	if maxMatches <= 0 {
		maxMatches = 10
	}
	if len(excerpts) > maxMatches {
		excerpts = excerpts[:maxMatches]
	}

	matches := make([]ContentMatch, len(excerpts))
	for i, e := range excerpts {
		matches[i] = ContentMatch{
			Text:  e.Text,
			Score: e.Score,
			Line:  e.Line,
		}
	}

	return matches
}

// scoreLine scores a single line against keywords.
func scoreLine(line string, keywords []string) float64 {
	lower := strings.ToLower(line)
	score := 0.0
	matches := 0

	for _, kw := range keywords {
		// Exact match bonus
		if strings.Contains(lower, kw) {
			score += 1.0
			matches++
			// Prefix/suffix matching for hyphenated/compound
			if strings.Contains(lower, " "+kw) || strings.Contains(lower, kw+" ") {
				score += 0.5
			}
		}
	}

	// Bonus for matching multiple keywords
	if matches > 1 {
		score *= (1.0 + 0.2*float64(matches-1))
	}

	// Penalize very long lines slightly (they're less focused)
	if len(line) > 200 {
		score *= 0.8
	}

	return score
}

// GenerateSummary creates a short summary of content.
func GenerateSummary(content string, keywords []string) string {
	lines := strings.Split(content, "\n")
	var nonEmpty []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}

	total := len(nonEmpty)
	if total == 0 {
		return "(empty)"
	}

	// Summary historically re-listed the top matches inline. The same lines
	// also ship in the response's Matches[] field, so the wire was paying
	// for them twice — and the inline listing was always the smaller, less
	// useful copy (truncated to 80 chars). Now Summary just states the
	// counts; the agent reads Matches[] for the actual lines.
	if len(keywords) > 0 {
		matches := MatchContent(content, keywords, 5)
		if len(matches) > 0 {
			return fmt.Sprintf("%d lines, %d matched", total, len(matches))
		}
		return fmt.Sprintf("%d lines, no matches for intent", total)
	}

	return fmt.Sprintf("%d lines", total)
}

// ExtractVocabulary returns distinctive terms from content. maxTerms caps the
// returned slice; <=0 falls back to 20, the historical default. The cap matters
// because vocab is a per-tool-call token cost — small outputs don't earn 20
// terms, large outputs may genuinely need them.
func ExtractVocabulary(content string, maxTerms int) []string {
	if maxTerms <= 0 {
		maxTerms = 20
	}
	freq := make(map[string]int)
	var current strings.Builder

	for _, r := range strings.ToLower(content) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 3 && current.Len() <= 32 {
				tok := current.String()
				if !isStopword(tok) {
					freq[tok]++
				}
			}
			current.Reset()
		}
	}

	if current.Len() >= 3 && current.Len() <= 32 {
		tok := current.String()
		if !isStopword(tok) {
			freq[tok]++
		}
	}

	// Score by TF-IDF-like metric: frequent but not too frequent
	type termScore struct {
		term  string
		score float64
	}
	var scores []termScore

	totalTokens := 0
	for _, c := range freq {
		totalTokens += c
	}

	for term, count := range freq {
		if count < 2 {
			continue
		}
		tf := float64(count) / float64(totalTokens)
		// Penalize extremely common terms
		idf := math.Log(float64(totalTokens) / float64(count))
		scores = append(scores, termScore{term: term, score: tf * idf})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	var vocab []string
	for i := 0; i < len(scores) && i < maxTerms; i++ {
		vocab = append(vocab, scores[i].term)
	}

	return vocab
}

// FilteredOutput is the result of applying a return-policy + intent filter
// to a raw body. The token-saving rule: only one of Body or
// (Summary+Matches+Vocabulary) is meaningful for any given response — the
// full raw body is never duplicated alongside its own excerpt.
type FilteredOutput struct {
	Body       string         // raw body, populated only when policy inlines
	Summary    string         // human-readable summary
	Matches    []ContentMatch // intent-matched excerpts
	Vocabulary []string       // distinctive-term vocabulary
}

// ApplyReturnPolicy is the single source of truth for how sandbox tools
// balance inline output against excerpts. It exists because the previous
// per-handler ad-hoc logic leaked the full body in three default cases:
//
//  1. empty intent -> full body inlined (the common case for naive agents)
//  2. intent provided but no matches -> full body inlined as fallback
//  3. "return" parameter parsed but never read
//
// Policy:
//
//	"raw"     - inline body, no excerpts. Agent pays full token cost.
//	"search"  - matches + vocabulary only. No body, no summary.
//	"summary" - summary + matches + vocabulary. Never inlines body.
//	"auto"/"" - inline body iff len(body) <= InlineThreshold (no excerpts;
//	            the body already contains everything matches/vocab would
//	            duplicate). Otherwise summary + matches + vocabulary,
//	            body dropped, with match/vocab counts scaled to size; when
//	            no matches landed, surface the last TailBytes as a "tail"
//	            hint so verdict-at-the-bottom output (test/build/CI) stays
//	            accessible.
//
// This makes the *default* path (auto + empty intent + large output) save
// tokens, instead of silently falling through to "return everything".
// Callers that need raw bytes must opt-in via Return: "raw".
func ApplyReturnPolicy(content, intent, returnMode string) FilteredOutput {
	keywords := ExtractKeywords(intent)
	out := FilteredOutput{}

	switch returnMode {
	case "raw":
		out.Body = content
		return out
	case "search":
		if len(keywords) > 0 {
			out.Matches = MatchContent(content, keywords, 10)
		}
		// Gate vocabulary when matches already cover >= half the budget.
		// At that point vocab tokens are mostly the same words the matches
		// surface, paying wire bytes for redundant signal. The threshold
		// (matchN/2) leaves room for vocab when matches are sparse and the
		// agent still needs a navigation hint.
		if len(out.Matches) < 5 {
			out.Vocabulary = ExtractVocabulary(content, 20)
		}
		return out
	case "summary":
		out.Summary = GenerateSummary(content, keywords)
		if len(keywords) > 0 {
			out.Matches = MatchContent(content, keywords, 10)
		}
		if len(out.Matches) < 5 {
			out.Vocabulary = ExtractVocabulary(content, 20)
		}
		return out
	default: // "auto" or ""
		// Inline-tier: body already carries everything. Adding summary +
		// matches + vocabulary on top would duplicate the same bytes the
		// agent is about to read inline — pure token waste. The agent can
		// scan the inlined body itself to satisfy the intent.
		if len(content) <= InlineThreshold {
			out.Body = content
			return out
		}
		// Mid-tier (4 KB – 64 KB): a handful of matches + a short vocab
		// is enough to navigate. Big-tier: the historical 10/20 caps.
		matchN, vocabN := 5, 10
		if len(content) > MediumThreshold {
			matchN, vocabN = 10, 20
		}
		out.Summary = GenerateSummary(content, keywords)
		var matches []ContentMatch
		if len(keywords) > 0 {
			matches = MatchContent(content, keywords, matchN)
		}
		// Kind-aware signal promotion: regardless of intent, surface
		// verdict-shaped lines (panic, FAIL, exception headers, error:
		// prefixes). Without this, an agent that ran `go test` with no
		// intent or with an intent that didn't happen to match the FAIL
		// lines got a vocabulary list and a tail snippet but no clear
		// pointer to the failures. Signals merge ahead of keyword
		// matches; the matchN budget caps the combined output.
		signals := extractSignalLines(content)
		out.Matches = mergeSignalsIntoMatches(signals, matches, matchN)
		// Same gate as search/summary: when matches fill at least half the
		// budget, vocab is duplicating signal the agent will already see.
		if len(out.Matches) < matchN/2 {
			out.Vocabulary = ExtractVocabulary(content, vocabN)
		}
		// Tail-bias: matches+summary+vocab miss the most common "useful
		// information lives at the end" pattern (go test, npm build, CI
		// run). Without this, the agent had to follow up with return=raw
		// to find out whether the run passed — the round trip cost more
		// than the tail it was looking for. Cap at TailBytes so we don't
		// re-introduce the original bloat. Signals already cover the
		// verdict lines in test/build output; tail is the fallback when
		// neither keywords nor signals landed.
		if len(out.Matches) == 0 {
			out.Body = tailLines(content, TailBytes)
		}
		return out
	}
}

// NormalizeOutput strips ANSI escape sequences, collapses carriage-return-
// rewritten lines down to their final state, and run-length-encodes
// long stretches of identical consecutive lines. It runs before the
// return-policy filter and the content-store stash so neither has to
// budget tokens for terminal animations.
//
// The three patterns this targets are the dominant noise sources in
// shell-captured output for an LLM:
//
//   - Color/style escapes (`\x1b[31m...\x1b[0m`) — visually meaningful in
//     a terminal, pure entropy in a token stream.
//   - Progress bars (`Downloading [###    ] 30%\r`) — the file-format
//     reads as one line that gets rewritten dozens of times; we keep
//     only the final state per line.
//   - Spinner / retry loops ("dialing...", "dialing...", ...) — N copies
//     compact to one + a "(repeated N times)" annotation.
func NormalizeOutput(s string) string {
	if s == "" {
		return s
	}
	// Binary refusal runs first: if the body is non-UTF-8 (PNG, PDF,
	// gzip, ELF, …) shipping the bytes as text wastes token budget
	// AND breaks JSON-RPC encoding. Replace with a one-line summary
	// the agent can reason about. Subsequent transforms (ANSI, JSON,
	// HTML compaction) are no-ops on the summary string.
	if compacted := CompactBinary(s); compacted != s {
		return compacted
	}
	s = stripANSI(s)
	s = collapseCarriageReturns(s)
	s = runLengthEncode(s)
	// Git diff `index` line drop: every file block in a `git diff`
	// emits an `index <hash>..<hash> <mode>` line that's wire-noise
	// for an LLM. CompactGitDiff drops them; no-op on non-diff input.
	s = CompactGitDiff(s)
	// Stack-trace path collapsing: when consecutive Python/Go frames
	// share a file path (recursive code), continuation frames get a
	// short marker instead of the full path repeated. Conservative
	// 3-frame threshold leaves flat traces untouched.
	s = CompactStackTracePaths(s)
	// Structured-output compaction (ADR-0010): when the body is a valid
	// JSON object/array — typical of `gh api`, `kubectl get -o json`,
	// `aws ... --output json` — drop hypermedia/timestamp noise fields.
	// CompactStructured is a no-op on non-JSON, partial JSON, or NDJSON,
	// so it's safe to run unconditionally.
	s = CompactStructured(s)
	// YAML companion: same drop-list, applied to YAML-shaped input
	// (kubectl -o yaml, helm get manifest). Detection is conservative
	// — only fires on `---` separators or apiVersion:/kind: headers.
	s = CompactYAML(s)
	// HTML → markdown conversion (ADR-0008 full path). When the body is
	// HTML-shaped, tokenize it and emit markdown — drops boilerplate
	// elements wholesale, converts headings/lists/code/links/tables to
	// markdown punctuation, and on cap-regression falls back to the
	// lite-path regex strip (CompactHTML) so we never inflate wire
	// bytes. ConvertHTML is detection-gated by the same leading
	// `<!doctype>` / `<html>` prefix CompactHTML used.
	s = ConvertHTML(s)
	return s
}

// stripANSI removes CSI and OSC escape sequences. Other categories
// (VT52, 7-bit single-shifts) are too rare in modern tool output to be
// worth pattern-matching; if they show up, the output is so unusual
// that the agent should opt into return=raw anyway.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiOSC.ReplaceAllString(s, "")
	return s
}

// collapseCarriageReturns reduces every "...A\rB\rC" run within a single
// logical line down to "C" — the last-written state is what a terminal
// user would have seen. The walk is per-line so a CR inside line 3
// doesn't eat content from line 1.
func collapseCarriageReturns(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		// Trim a trailing CR (CRLF leftover) before scanning, otherwise a
		// well-formed CRLF file would have every line collapsed to "".
		ln = strings.TrimRight(ln, "\r")
		if idx := strings.LastIndex(ln, "\r"); idx >= 0 {
			ln = ln[idx+1:]
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

// runLengthEncode replaces stretches of >= rleMinReps identical adjacent
// lines with a single copy plus a short annotation. The minimum threshold
// is there because the annotation itself costs tokens — squashing two
// duplicates makes the output longer, not shorter.
func runLengthEncode(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		out = append(out, lines[i])
		reps := j - i
		if reps >= rleMinReps {
			out = append(out, fmt.Sprintf("... (line above repeated %d more times)", reps-1))
			i = j
			continue
		}
		i++
	}
	return strings.Join(out, "\n")
}

// tailLines returns the last maxBytes of s, aligned to a newline boundary
// so we don't cut mid-line, and prefixed with a marker the agent can
// recognize. When s is already <= maxBytes we return it untouched.
func tailLines(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := len(s) - maxBytes
	// Walk forward to the next newline so the snippet starts on a clean
	// line. If there's no newline in the candidate region, fall back to
	// the byte cut and trim any trailing partial rune.
	if nl := strings.IndexByte(s[cut:], '\n'); nl >= 0 && cut+nl+1 < len(s) {
		cut = cut + nl + 1
	} else {
		// Walk back to a UTF-8 rune-start so encoding/json doesn't emit
		// U+FFFD into the marker line.
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}
	return "...(tail; earlier output dropped)\n" + s[cut:]
}

// truncate shortens s to at most maxLen bytes, appending "..." when it had
// to cut. The cut is backed up to a rune boundary so non-ASCII summaries
// (Turkish, CJK, accented Latin) don't end mid-rune — encoding/json would
// otherwise substitute U+FFFD for the invalid UTF-8 tail.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// For maxLen < 3 there's no room for the ellipsis; just return a valid
	// rune-aligned prefix.
	cut := maxLen - 3
	if cut <= 0 {
		cut = maxLen
	}
	if cut > len(s) {
		cut = len(s)
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut >= maxLen {
		return s[:cut]
	}
	return s[:cut] + "..."
}

// trimPartialRune drops a trailing partial UTF-8 rune if one is present.
// Use when a hard byte cap may have cut across a multi-byte character —
// without trimming, encoding/json substitutes U+FFFD for the orphan
// continuation bytes and the consumer sees a mangled last character.
func trimPartialRune(s string) string {
	if s == "" {
		return s
	}
	// Walk back from the end across continuation bytes (10xxxxxx). If the
	// rune-start byte's expected length exceeds what's actually present at
	// the tail, the rune is incomplete and must be dropped.
	end := len(s)
	cut := end - 1
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	expected := expectedRuneLen(s[cut])
	if expected == 0 || cut+expected == end {
		return s
	}
	return s[:cut]
}

// expectedRuneLen returns the byte length implied by the leading byte of a
// UTF-8 rune, or 0 if b is not a valid rune-start.
func expectedRuneLen(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	}
	return 0
}
