package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/sandbox"
)

func wireJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLeanRead_OneLineWholeFile(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{
		Content: "hello\n", RawContent: "hello\n", Size: 6, ReadBytes: 6,
		StartLine: 1, EndLine: 1, TotalLines: 1,
	}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Read(context.Background(), ReadParams{Path: "a.txt", Intent: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := wireJSON(t, r), `{"content":"hello\n"}`; got != want {
		t.Errorf("lean read = %s, want %s", got, want)
	}
}

func TestLeanRead_PartialWindowKeepsLines(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{
		Content: "b\n", RawContent: "b\n", Size: 100, ReadBytes: 2,
		StartLine: 2, EndLine: 2, TotalLines: 0,
	}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Read(context.Background(), ReadParams{Path: "a.txt", Offset: 2, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.StartLine != 2 || r.EndLine != 2 {
		t.Errorf("partial window lost its line range: %+v", r)
	}
	if r.ContentID != "" || r.Size != 0 || r.ReadBytes != 0 {
		t.Errorf("bookkeeping should still be dropped: %+v", r)
	}
}

func TestLeanRead_EmptyFileKeepsMetadata(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{Size: 0}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Read(context.Background(), ReadParams{Path: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := wireJSON(t, r), `{"size":0,"read_bytes":0}`; got != want {
		t.Errorf("empty read = %s, want %s", got, want)
	}
}

func TestLeanRead_ExcerptedAndLargeKeepFullShape(t *testing.T) {
	big := strings.Repeat("x", 8000) // > 1024 tokens
	sb := &stubSandbox{readResp: sandbox.ReadResp{
		Content: big, RawContent: big, Size: 8000, ReadBytes: 8000,
		StartLine: 1, EndLine: 1, TotalLines: 1,
	}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Read(context.Background(), ReadParams{Path: "big", Return: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ContentID == "" || r.Size != 8000 || r.ReadBytes != 8000 {
		t.Errorf("large body must keep the full shape: %+v", r)
	}

	sb.readResp = sandbox.ReadResp{
		Content: "small", RawContent: "small body", Excerpted: true, Size: 10, ReadBytes: 10,
	}
	r, err = h.Read(context.Background(), ReadParams{Path: "ex", Intent: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ContentID == "" || r.Size != 10 {
		t.Errorf("excerpted reply must keep the full shape: %+v", r)
	}
}

func TestLeanExec_EchoAndSilentSuccess(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{
		Exit: 0, Stdout: "hi\n", RawStdout: "hi\n", DurationMs: 7,
	}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Exec(context.Background(), ExecParams{Code: "echo hi", Lang: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := wireJSON(t, r), `{"exit":0,"stdout":"hi\n"}`; got != want {
		t.Errorf("lean exec = %s, want %s", got, want)
	}

	sb.execResp = sandbox.ExecResp{Exit: 0, DurationMs: 25}
	r, err = h.Exec(context.Background(), ExecParams{Code: "true", Lang: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := wireJSON(t, r), `{"exit":0}`; got != want {
		t.Errorf("silent success must still carry exit:0, got %s", got)
	}
}

func TestLeanExec_TimedOutAndFailureSurvive(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{
		Exit: 124, Stderr: "boom", TimedOut: true, DurationMs: 3000,
	}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Exec(context.Background(), ExecParams{Code: "sleep 9", Lang: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	js := wireJSON(t, r)
	if !strings.Contains(js, `"exit":124`) || !strings.Contains(js, `"timed_out":true`) {
		t.Errorf("exit/timed_out must survive lean shaping: %s", js)
	}
}

func TestLeanExec_SummaryKeepsFullShape(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{
		Exit: 0, Summary: "3 lines", RawStdout: "a\nb\nc\n", DurationMs: 12,
	}}
	h := newWireDedupHandlers(t, sb)
	r, err := h.Exec(context.Background(), ExecParams{Code: "x", Lang: "bash", Intent: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ContentID == "" || r.DurationMs != 12 {
		t.Errorf("summary reply must keep content_id/duration: %+v", r)
	}
}

func TestLeanFetch_DropsContentIDKeepsStatus(t *testing.T) {
	r := leanFetch(&FetchResponse{Status: 200, Body: "ok", ContentID: "abc"}, false)
	if r.ContentID != "" || r.Status != 200 {
		t.Errorf("unexpected: %+v", r)
	}
	r = leanFetch(&FetchResponse{Status: 200, Body: "ok", ContentID: "abc"}, true)
	if r.ContentID == "" {
		t.Errorf("excerpted fetch must keep content_id")
	}
}
