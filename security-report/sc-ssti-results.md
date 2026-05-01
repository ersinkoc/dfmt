# SC-SSTI: Server-Side Template Injection — DFMT Assessment

**Skill version:** 1.0.0  
**Target:** `D:\Codebox\PROJECTS\DFMT`  
**Date:** 2026-05-01  
**Engineer:** sc-ssti

---

## 1. Discovery Summary

| Surface | Present | Engine | Risk |
|---------|---------|--------|------|
| HTTP dashboard | YES | None (static HTML strings) | NONE |
| `html/template` usage | NONE | — | — |
| `text/template` usage | NONE | — | — |
| `template.HTML` to bypass escaping | NONE | — | — |
| Markdown rendering | YES | Custom walker (`htmlmd.go`) | LOW |
| User input in template strings | NONE | — | — |

**Overall verdict: NO SSTI VULNERABILITY.**

---

## 2. HTTP Dashboard — No Template Engine

**File:** `internal/transport/dashboard.go`

The dashboard HTML and JavaScript are static string constants embedded in the binary at compile time:

```go
const DashboardHTML = `<!DOCTYPE html>...`
const DashboardJS  = `(function() {...})()`
```

They are served via direct byte write:

```go
// internal/transport/http.go:621
_, _ = w.Write([]byte(DashboardHTML))  // handleDashboard

// internal/transport/http.go:629
_, _ = w.Write([]byte(DashboardJS))    // handleDashboardJS
```

**No template engine** (`html/template`, `text/template`, or any third-party library) is used to render the dashboard. No user-controlled data is interpolated into these strings.

---

## 3. `html/template` / `template.HTML` Usage — NONE FOUND

Grepped all `.go` files for:

- `template.New`, `text/template`, `html/template`
- `template.Execute`, `template.ExecuteTemplate`
- `template.HTML` (the escape bypass type)

**Zero matches.** The codebase does not use Go's `html/template` package at all. This is consistent with the stated design: Go uses `html/template` for HTML auto-escaping of user-facing templates, which DFMT does not employ.

---

## 4. User Input Flow — No Template String Interpolation

All JSON-RPC handlers (`dfmt.remember`, `dfmt.search`, `dfmt.recall`, `dfmt.stats`, etc.) receive user input as structured data parameters via `json.Unmarshal` into typed parameter structs. They pass data to internal business logic — not to any template engine.

Example (HTTP handler, `http.go:420-450`):
```go
var resp any
switch req.Method {
case methodRemember, aliasRemember:
    resp = s.handleRemember(ctx, req)
    // ... user input unmarshalled into RememberParams struct,
    // then passed to s.handlers.Remember(ctx, params)
case methodSearch, aliasSearch:
    resp = s.handleSearch(ctx, req)
// ...
}
```

There is no `render_template_string`-equivalent pattern.

---

## 5. Markdown Rendering — Custom Walker, Not a Template Engine

### 5a. HTML-to-Markdown Walker (`internal/sandbox/htmlmd.go`)

`ConvertHTML(s)` consumes an HTML token stream and emits markdown. It is detection-gated — only fires when input starts with `<!doctype html>` or `<html>`.

**Security-relevant design choices:**
- `htmlDropElements` map drops `<script>`, `<style>`, `<nav>`, `<footer>`, `<aside>`, `<head>`, `<noscript>`, `<svg>`, `<form>`, `<button>`, `<iframe>` wholesale — no markdown emitted for these elements at all.
- `<a>` tags emit as markdown `[linktext](href)` where `href` is sourced from the token's attribute map. URLs are not validated, but they are not re-embedded as HTML or JavaScript.
- No `template.HTML` is used to force raw HTML through.
- Cap-regression guard: if markdown output >= input bytes, falls back to `CompactHTML` regex strip, never returns more bytes than came in.

**Risk assessment: LOW.** The walker converts HTML to markdown (a safe output format). It does not emit HTML or JavaScript back. A malicious HTML page cannot cause script injection through this path because the output is always markdown punctuated text.

### 5b. Markdown Renderer (`internal/retrieve/render_md.go`)

`MarkdownRenderer.Render(snap)` builds a markdown snapshot using `fmt.Fprintf` into a `strings.Builder`. **No template engine is used.**

```go
// internal/retrieve/render_md.go:36-38
b.WriteString("# Session Snapshot\n\n")
fmt.Fprintf(&b, "**Events:** %d | **Size:** %d bytes | **Tiers:** %s\n\n",
    len(snap.Events), snap.ByteSize, strings.Join(snap.TierOrder, ", "))
```

Event fields (Type, Actor, ID, message, path) are written as plain strings. The path reference table (`[rNN]` tokens) is built from a map with no user-controlled interpolation into template syntax.

### 5c. XML Renderer (`internal/retrieve/render_md.go:216-222`)

Untrusted event fields are explicitly escaped via `xml.EscapeText`:

```go
func xmlEscape(s string) string {
    var b strings.Builder
    if err := xml.EscapeText(&b, []byte(s)); err != nil {
        return ""
    }
    return b.String()
}
```

This is the correct defensive pattern for untrusted data in XML output.

---

## 6. Finding: SSTI-001 — Not Applicable

- **Title:** Server-Side Template Injection — Not Present
- **Severity:** N/A
- **Confidence:** 100%
- **File:** N/A
- **Vulnerability Type:** N/A
- **Description:** DFMT does not use any server-side template engine. The HTTP dashboard serves static embedded strings. Markdown is produced by a custom HTML→markdown walker and `fmt.Fprintf`-based string builders, not by a template engine. No user input is ever interpolated into template strings.
- **Proof of Concept:** N/A — no vulnerability present.
- **Impact:** N/A
- **Remediation:** None required.
- **References:** https://cwe.mitre.org/data/definitions/1336.html

---

## 7. Findings Summary

| ID | Title | Severity | Confidence |
|----|-------|---------|------------|
| SSTI-001 | No template engine in use — not vulnerable | N/A | High |

**Total: 1 item — Not a vulnerability (no attack surface).**

---

## 8. Related Security Controls Observed

- **F-09**: Non-loopback HTTP bind refused — unauthenticated JSON-RPC is loopback-only.
- **V-16**: `/api/daemons` returns empty list when `projectPath` not set (fail-closed).
- **V-17**: Same-origin enforcement on dashboard endpoints via `isAllowedOrigin`.
- **Host header validation** defends against DNS-rebinding attacks.
- **`html/template` not used** — the codebase correctly avoids it since there are no user-facing Go templates.

---

## 9. Conclusion

DFMT has **no Server-Side Template Injection attack surface**. The HTTP dashboard uses static embedded HTML/JS with no templating. The HTML→markdown conversion pipeline uses a custom walker that strips interactive elements and outputs only markdown punctuation. No Go `template` package is used anywhere in the codebase.