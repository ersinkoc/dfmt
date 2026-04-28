package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// tokenSavingScenario is one "what an agent would see" simulation. Body is
// the raw stdout/file/HTTP response the sandbox would have captured before
// any of the token-saving layers ran. Intent is what the agent passed (may
// be empty to model the lazy-caller path that the original 7-change plan
// existed to fix).
type tokenSavingScenario struct {
	name   string
	body   string
	intent string
}

// runTokenSavingReport renders a side-by-side comparison of legacy vs.
// modern wire bytes for representative sandbox outputs. "Legacy" means the
// pre-overhaul behavior: no NormalizeOutput, ApplyReturnPolicy on raw
// bytes, MCP envelope duplicates the JSON payload into both content[0].
// text and structuredContent. "Modern" means the post-overhaul defaults.
//
// The numbers are pure JSON byte counts — i.e. exactly what the MCP
// transport writes to the wire, not abstract "savings %". Agents pay for
// these bytes whether or not the model tokenizer compresses them further.
func runTokenSavingReport() {
	scenarios := buildScenarios()

	fmt.Println("\n=== Token-Saving Wire-Bytes Report ===")
	fmt.Printf("%-44s %10s %10s %10s %10s\n", "Scenario", "Raw", "Legacy", "Modern", "Savings")
	fmt.Println(strings.Repeat("-", 90))
	totalLegacy, totalModern := 0, 0
	for _, sc := range scenarios {
		legacy := legacyWireBytes(sc.body, sc.intent)
		modern := modernWireBytes(sc.body, sc.intent)
		saved := 100.0 * float64(legacy-modern) / float64(legacy)
		fmt.Printf("%-44s %10d %10d %10d %9.1f%%\n",
			truncName(sc.name, 44), len(sc.body), legacy, modern, saved)
		totalLegacy += legacy
		totalModern += modern
	}
	fmt.Println(strings.Repeat("-", 90))
	totalSaved := 100.0 * float64(totalLegacy-totalModern) / float64(totalLegacy)
	fmt.Printf("%-44s %10s %10d %10d %9.1f%%\n", "TOTAL", "", totalLegacy, totalModern, totalSaved)
	fmt.Println()
}

// buildScenarios returns the canonical workload set: progress-bar UI, retry
// loop, language-specific test/build failures with intent-less calls (the
// case where signal extraction earns its keep), and a small file read where
// inline-tier gating is the dominant win.
func buildScenarios() []tokenSavingScenario {
	var npmInstall strings.Builder
	for i := 0; i <= 100; i += 5 {
		npmInstall.WriteString(fmt.Sprintf("\rnpm install \x1b[34m[%-20s]\x1b[0m %d%%",
			strings.Repeat("#", i/5), i))
	}
	npmInstall.WriteString("\nadded 1247 packages in 18s\n")

	var spinner strings.Builder
	for i := 0; i < 50; i++ {
		spinner.WriteString("dialing host gateway-prod-eu-west-3.example.com:5432...\n")
	}
	spinner.WriteString("connected\n")

	var goTest strings.Builder
	for i := 0; i < 200; i++ {
		goTest.WriteString(fmt.Sprintf("=== RUN   TestSomething%d\n--- PASS: TestSomething%d (0.00s)\n", i, i))
	}
	goTest.WriteString("--- FAIL: TestRegression (0.02s)\n    regression_test.go:88: nil pointer dereference\n")
	goTest.WriteString("panic: runtime error: invalid memory address\n")
	goTest.WriteString("FAIL\tgithub.com/example/pkg\t1.234s\n")

	var pytest strings.Builder
	for i := 0; i < 200; i++ {
		pytest.WriteString(fmt.Sprintf("tests/test_module.py::test_case_%d PASSED\n", i))
	}
	pytest.WriteString("tests/test_module.py::test_failure FAILED\n")
	pytest.WriteString("Traceback (most recent call last):\n")
	pytest.WriteString("  File \"tests/test_module.py\", line 42, in test_failure\n")
	pytest.WriteString("AssertionError: 5 != 3\n")

	var cargo strings.Builder
	for i := 0; i < 250; i++ {
		cargo.WriteString(fmt.Sprintf("   Compiling some_crate v0.%d.0\n", i))
	}
	cargo.WriteString("error[E0277]: the trait bound `String: Copy` is not satisfied\n")
	cargo.WriteString("error: could not compile `some_crate` due to previous error\n")

	smallFile := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"

	// gh api issue list — exercises ADR-0010's structured-output compaction.
	// Each issue carries the canonical Github noise fields (URL family,
	// timestamps, etag, _links) that the compactor strips before the policy
	// filter sees the body.
	var ghIssues strings.Builder
	ghIssues.WriteString("[")
	for i := 1; i <= 50; i++ {
		if i > 1 {
			ghIssues.WriteString(",")
		}
		ghIssues.WriteString(fmt.Sprintf(`{`+
			`"id":%d,`+
			`"node_id":"MDU6SXNzdWUx%d",`+
			`"url":"https://api.github.com/repos/foo/bar/issues/%d",`+
			`"html_url":"https://github.com/foo/bar/issues/%d",`+
			`"events_url":"https://api.github.com/repos/foo/bar/issues/%d/events",`+
			`"labels_url":"https://api.github.com/repos/foo/bar/issues/%d/labels{/name}",`+
			`"comments_url":"https://api.github.com/repos/foo/bar/issues/%d/comments",`+
			`"repository_url":"https://api.github.com/repos/foo/bar",`+
			`"number":%d,`+
			`"title":"Bug %d in parser",`+
			`"state":"open",`+
			`"body":"The parser crashes on input %d.",`+
			`"created_at":"2024-01-15T10:00:00Z",`+
			`"updated_at":"2024-02-20T14:30:00Z",`+
			`"etag":"W/\"abc%d\"",`+
			`"_links":{"self":{"href":"..."}}`+
			`}`,
			i, i, i, i, i, i, i, i, i, i, i))
	}
	ghIssues.WriteString("]")

	return []tokenSavingScenario{
		{name: "small file read (inline tier)", body: smallFile, intent: "main function"},
		{name: "npm install with progress bar", body: npmInstall.String(), intent: ""},
		{name: "spinner retry-loop spam", body: spinner.String(), intent: ""},
		{name: "go test 200 PASS + 1 FAIL + panic", body: goTest.String(), intent: ""},
		{name: "pytest 200 PASS + 1 FAIL + traceback", body: pytest.String(), intent: ""},
		{name: "cargo build 250 compile + 2 errors", body: cargo.String(), intent: ""},
		{name: "gh api 50 issues (structured noise)", body: ghIssues.String(), intent: ""},
	}
}

// legacyWireBytes simulates the pre-overhaul pipeline:
//   - No NormalizeOutput (raw body straight into the policy filter)
//   - Old auto path: small bodies inlined with ALL excerpts; large bodies
//     dropped body and surfaced summary+matches+vocab. This is reproduced
//     here by calling ApplyReturnPolicy in the modern code (its inline
//     gating is now part of the filter) and then re-inflating the response
//     with the legacy MCP envelope (full payload duplicated into
//     content[0].text alongside structuredContent).
//
// The legacy result is INTENTIONALLY a slight underestimate vs reality —
// real legacy inlined the body even on large outputs when intent was
// empty (the leak the project was created to fix). Modeling that exactly
// requires the pre-overhaul ApplyReturnPolicy, which no longer exists.
// What we DO faithfully model is the MCP envelope duplication, which was
// always present and was a flat ~50% tax independent of body size.
func legacyWireBytes(body, intent string) int {
	out := sandbox.ApplyReturnPolicy(body, intent, "auto")
	payload := wirePayload(out)
	body2 := mustJSON(payload)
	// Legacy: content[0].text holds the full JSON and structuredContent
	// holds the same payload. Both are JSON-marshaled together by the
	// transport.
	envelope := struct {
		Content           []map[string]string `json:"content"`
		StructuredContent any                 `json:"structuredContent"`
	}{
		Content:           []map[string]string{{"type": "text", "text": string(body2)}},
		StructuredContent: payload,
	}
	return len(mustJSON(envelope))
}

// modernWireBytes simulates the current pipeline:
//   - NormalizeOutput on raw bytes first
//   - ApplyReturnPolicy with all the new gating/tail-bias/signal logic
//   - MCP envelope: 27-byte sentinel in content[0].text, payload only in
//     structuredContent.
func modernWireBytes(body, intent string) int {
	normalized := sandbox.NormalizeOutput(body)
	out := sandbox.ApplyReturnPolicy(normalized, intent, "auto")
	payload := wirePayload(out)
	envelope := struct {
		Content           []map[string]string `json:"content"`
		StructuredContent any                 `json:"structuredContent"`
	}{
		Content:           []map[string]string{{"type": "text", "text": "dfmt: see structuredContent"}},
		StructuredContent: payload,
	}
	return len(mustJSON(envelope))
}

// wirePayload converts an ApplyReturnPolicy result into the same struct
// shape ExecResponse / ReadResponse / FetchResponse use on the wire. We
// model the Exec response since its field set covers the union of the
// three (Stdout/Body, Summary, Matches, Vocabulary, ContentID).
func wirePayload(out sandbox.FilteredOutput) map[string]any {
	p := map[string]any{}
	if out.Body != "" {
		p["stdout"] = out.Body
	}
	if out.Summary != "" {
		p["summary"] = out.Summary
	}
	if len(out.Matches) > 0 {
		p["matches"] = out.Matches
	}
	if len(out.Vocabulary) > 0 {
		p["vocabulary"] = out.Vocabulary
	}
	// content_id placeholder — its 26-byte ULID is the same length under
	// both paths, included so envelope sizes are realistic.
	p["content_id"] = "01HXYZ1234567890ABCDEFGHIJ"
	p["exit"] = 0
	p["duration_ms"] = 42
	return p
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprintf("%v", v))
	}
	return b
}

func truncName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
