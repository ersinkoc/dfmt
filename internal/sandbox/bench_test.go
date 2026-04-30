package sandbox

import (
	"strings"
	"testing"
)

// Benchmarks for hot paths on the response side. None of these run in CI
// (`go test -bench` is opt-in); the Makefile's `make bench` target picks
// them up alongside cmd/dfmt-bench. Useful as a regression baseline when
// touching the NormalizeOutput pipeline, the HTML walker, or the policy
// evaluator.

func BenchmarkApproxTokens_ASCII(b *testing.B) {
	s := strings.Repeat("the quick brown fox jumps over the lazy dog ", 256)
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = ApproxTokens(s)
	}
}

func BenchmarkApproxTokens_CJK(b *testing.B) {
	s := strings.Repeat("你好世界今日天气真好我们一起去公园散步吧", 256)
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = ApproxTokens(s)
	}
}

func BenchmarkApproxTokens_Mixed(b *testing.B) {
	s := strings.Repeat("hello 你好 dünya merhaba 世界 ", 256)
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = ApproxTokens(s)
	}
}

func BenchmarkNormalizeOutput_PlainText(b *testing.B) {
	s := strings.Repeat("ordinary log line without any escape sequences\n", 200)
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = NormalizeOutput(s)
	}
}

func BenchmarkNormalizeOutput_HeavyANSI(b *testing.B) {
	one := "\x1b[31mERROR\x1b[0m \x1b[1mbold\x1b[22m \x1b[33mwarn\x1b[0m line\n"
	s := strings.Repeat(one, 200)
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = NormalizeOutput(s)
	}
}

func BenchmarkNormalizeOutput_RepeatedLines(b *testing.B) {
	// Exercises the RLE stage. 1000 identical lines collapse to a single
	// "(repeated N times)" annotation.
	s := strings.Repeat("connection refused\n", 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = NormalizeOutput(s)
	}
}

func BenchmarkConvertHTML_Small(b *testing.B) {
	s := `<html><body>
<h1>Title</h1>
<p>A paragraph with <a href="https://example.com">a link</a> and <strong>emphasis</strong>.</p>
<ul><li>one</li><li>two</li><li>three</li></ul>
</body></html>`
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = ConvertHTML(s)
	}
}

func BenchmarkConvertHTML_Table(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("<table><thead><tr><th>A</th><th>B</th><th>C</th></tr></thead><tbody>")
	for i := 0; i < 100; i++ {
		sb.WriteString("<tr><td>cell-a</td><td>cell-b</td><td>cell-c</td></tr>")
	}
	sb.WriteString("</tbody></table>")
	s := sb.String()
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = ConvertHTML(s)
	}
}

func BenchmarkConvertHTML_Heavy(b *testing.B) {
	// Realistic doc-page shape: nav + script (dropped wholesale) + content.
	var sb strings.Builder
	sb.WriteString("<html><head><script>alert(1)</script><style>x{}</style></head><body>")
	sb.WriteString("<nav>nav-content-to-drop</nav>")
	for i := 0; i < 50; i++ {
		sb.WriteString("<h2>Section</h2><p>Body with <code>inline-code</code> text.</p>")
		sb.WriteString("<pre><code class=\"language-go\">func main() {}</code></pre>")
	}
	sb.WriteString("<footer>footer</footer></body></html>")
	s := sb.String()
	b.ReportAllocs()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = ConvertHTML(s)
	}
}

func BenchmarkPolicyEvaluate_ExecAllow(b *testing.B) {
	p := DefaultPolicy()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = p.Evaluate("exec", "git status")
	}
}

func BenchmarkPolicyEvaluate_ReadDeny(b *testing.B) {
	// Deny is checked first; exercise a path that hits a deny rule.
	p := DefaultPolicy()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = p.Evaluate("read", "src/.env.local")
	}
}

func BenchmarkPolicyEvaluate_ReadAllow(b *testing.B) {
	// Common case: read of an ordinary project file. Default policy has
	// no read allow rules so the result is "allowed by default".
	p := DefaultPolicy()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = p.Evaluate("read", "internal/core/journal.go")
	}
}

func BenchmarkMergePolicies(b *testing.B) {
	base := DefaultPolicy()
	override := Policy{
		Allow: []Rule{
			{Op: "exec", Text: "my-build *"},
			{Op: "read", Text: "creds/**"},
			{Op: "exec", Text: "rm *"}, // hard-deny masked
		},
		Deny: []Rule{
			{Op: "read", Text: "**/private/**"},
		},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = MergePolicies(base, override)
	}
}
