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

	// kubectl get pods -o json — k8s objects carry deep metadata blocks
	// (creationTimestamp, resourceVersion, selfLink, uid, ownerReferences,
	// managedFields). The structured compactor catches the URL family,
	// timestamps, and node_id; managedFields stays because it's not on
	// the drop list (a richer drop list could halve this further — out
	// of scope for ADR-0010's conservative default).
	var kubectl strings.Builder
	kubectl.WriteString(`{"apiVersion":"v1","kind":"List","items":[`)
	for i := 1; i <= 20; i++ {
		if i > 1 {
			kubectl.WriteString(",")
		}
		kubectl.WriteString(fmt.Sprintf(`{`+
			`"apiVersion":"v1",`+
			`"kind":"Pod",`+
			`"metadata":{`+
			`"name":"worker-%d","namespace":"prod",`+
			`"uid":"abc-1234-def-%d",`+
			`"resourceVersion":"99887%d",`+
			`"creationTimestamp":"2024-03-15T08:30:00Z",`+
			`"selfLink":"/api/v1/namespaces/prod/pods/worker-%d",`+
			`"labels":{"app":"worker","tier":"backend"}`+
			`},`+
			`"spec":{"containers":[{"name":"app","image":"acme/worker:v1.2.%d"}]},`+
			`"status":{"phase":"Running","podIP":"10.0.0.%d"}`+
			`}`,
			i, i, i, i, i, i))
	}
	kubectl.WriteString(`]}`)

	// aws ec2 describe-instances — Reservations[].Instances[] shape with
	// the typical noise (LaunchTime, NetworkInterfaces[].Attachment.AttachTime,
	// IamInstanceProfile.Arn, etc.). Most agent reasoning over EC2 data
	// keys off InstanceId + State + InstanceType + private-IP; the rest
	// is wire bloat.
	var awsEC2 strings.Builder
	awsEC2.WriteString(`{"Reservations":[`)
	for i := 1; i <= 15; i++ {
		if i > 1 {
			awsEC2.WriteString(",")
		}
		awsEC2.WriteString(fmt.Sprintf(`{`+
			`"ReservationId":"r-abc%d",`+
			`"OwnerId":"123456789012",`+
			`"Instances":[{`+
			`"InstanceId":"i-0abc%d",`+
			`"InstanceType":"t3.medium",`+
			`"State":{"Name":"running","Code":16},`+
			`"PrivateIpAddress":"10.1.0.%d",`+
			`"PublicDnsName":"",`+
			`"LaunchTime":"2024-01-15T10:00:00Z",`+
			`"VpcId":"vpc-aaa","SubnetId":"subnet-bbb",`+
			`"node_id":"opaque-%d",`+
			`"created_at":"2024-01-15T10:00:00Z",`+
			`"updated_at":"2024-04-01T12:00:00Z",`+
			`"console_url":"https://console.aws.amazon.com/ec2/v2/home?instanceId=i-%d",`+
			`"metadata_url":"http://169.254.169.254/latest/meta-data/i-%d"`+
			`}]}`,
			i, i, i, i, i, i))
	}
	awsEC2.WriteString(`]}`)

	// kubectl get pods -o json | jq -c '.items[]' — NDJSON shape, one
	// pod per line. Exercises ADR-0010's NDJSON path; without it the
	// body falls through structured detection (multi-root → not valid
	// JSON) and ships uncompacted.
	var ndjson strings.Builder
	for i := 1; i <= 30; i++ {
		ndjson.WriteString(fmt.Sprintf(`{`+
			`"apiVersion":"v1","kind":"Pod",`+
			`"metadata":{`+
			`"name":"worker-%d","namespace":"prod",`+
			`"uid":"abc-%d","resourceVersion":"99887%d",`+
			`"creationTimestamp":"2024-03-15T08:30:00Z",`+
			`"selfLink":"/api/v1/namespaces/prod/pods/worker-%d"`+
			`},`+
			`"status":{"phase":"Running","podIP":"10.0.0.%d"}`+
			`}`+"\n",
			i, i, i, i, i))
	}

	return []tokenSavingScenario{
		{name: "small file read (inline tier)", body: smallFile, intent: "main function"},
		{name: "npm install with progress bar", body: npmInstall.String(), intent: ""},
		{name: "spinner retry-loop spam", body: spinner.String(), intent: ""},
		{name: "go test 200 PASS + 1 FAIL + panic", body: goTest.String(), intent: ""},
		{name: "pytest 200 PASS + 1 FAIL + traceback", body: pytest.String(), intent: ""},
		{name: "cargo build 250 compile + 2 errors", body: cargo.String(), intent: ""},
		{name: "gh api 50 issues (structured noise)", body: ghIssues.String(), intent: ""},
		{name: "kubectl get 20 pods -o json", body: kubectl.String(), intent: ""},
		{name: "aws ec2 describe 15 instances", body: awsEC2.String(), intent: ""},
		{name: "kubectl ... | jq -c .items[] (NDJSON)", body: ndjson.String(), intent: ""},
		{name: "fetched doc page (HTML boilerplate)", body: htmlDocPage(), intent: ""},
	}
}

// htmlDocPage builds a representative documentation/marketing page shape:
// most of the wire is chrome (analytics, css, nav, footer, sidebar) with
// a small content island in <main>. Exercises ADR-0008's lite path.
func htmlDocPage() string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<title>API Reference - Foo Library</title>
<meta charset="utf-8">
<link rel="stylesheet" href="/static/main.css">
<style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;padding:0}
.nav{background:#fff;border-bottom:1px solid #e0e0e0;padding:1rem 2rem}
.sidebar{width:280px;float:left;background:#f5f5f5;padding:2rem;min-height:100vh}
.content{margin-left:280px;padding:2rem}
.footer{background:#222;color:#aaa;padding:3rem 2rem;font-size:0.9rem}
`)
	for i := 0; i < 30; i++ {
		b.WriteString(`.cls-` + fmt.Sprint(i) + `{display:block;margin:0.5rem 0;color:#333}`)
	}
	b.WriteString(`</style>
<script>
(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':new Date().getTime(),event:'gtm.js'});
var f=d.getElementsByTagName(s)[0],j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';
j.async=true;j.src='https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);
})(window,document,'script','dataLayer','GTM-XXXXXX');
</script>
</head>
<body>
<nav class="nav">
<ul><li><a href="/">Home</a></li><li><a href="/docs">Docs</a></li><li><a href="/api">API</a></li><li><a href="/blog">Blog</a></li><li><a href="/about">About</a></li></ul>
</nav>
<aside class="sidebar">
<h3>API Reference</h3>
<ul>
`)
	for i := 0; i < 25; i++ {
		b.WriteString(`<li><a href="/api/method-` + fmt.Sprint(i) + `">method ` + fmt.Sprint(i) + `</a></li>`)
	}
	b.WriteString(`</ul>
</aside>
<main class="content">
<h1>parseConfig(input, options)</h1>
<p>Parses a configuration string and returns a structured Config object. Throws <code>ParseError</code> on malformed input.</p>
<h2>Parameters</h2>
<ul>
<li><code>input</code> (string) - The configuration source.</li>
<li><code>options</code> (object) - Parser options. <code>strict</code> (boolean, default true) enables strict-mode validation.</li>
</ul>
<h2>Returns</h2>
<p>A <code>Config</code> object on success. Throws <code>ParseError</code> with a <code>line</code> and <code>column</code> property on failure.</p>
<h2>Example</h2>
<pre><code>const cfg = parseConfig(src, { strict: false });
console.log(cfg.section('database').get('host'));</code></pre>
</main>
<footer class="footer">
<div class="footer-cols">
<div><h4>Product</h4><ul><li>Features</li><li>Pricing</li><li>Roadmap</li></ul></div>
<div><h4>Company</h4><ul><li>About</li><li>Blog</li><li>Careers</li></ul></div>
<div><h4>Legal</h4><ul><li>Privacy</li><li>Terms</li><li>Cookies</li></ul></div>
</div>
<p>&copy; 2024 Foo Library. All rights reserved.</p>
</footer>
<script>window.analytics=window.analytics||[];analytics.track('pageview');</script>
<script async src="https://cdn.example.com/widget.js"></script>
</body>
</html>`)
	return b.String()
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
