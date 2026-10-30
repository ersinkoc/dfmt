package main

import (
	"strings"
	"testing"
)

func TestReplayJournal(t *testing.T) {
	journal := strings.Join([]string{
		`{"id":"1","type":"tool.exec","data":{"raw_bytes":40000,"returned_bytes":4000}}`,
		`{"id":"2","type":"tool.exec","data":{"raw_bytes":100,"returned_bytes":100}}`,
		`{"id":"3","type":"tool.grep","data":{"raw_bytes":200,"returned_bytes":200}}`,
		`{"id":"4","type":"note","data":{"raw_bytes":999999,"returned_bytes":1}}`,
		`{"id":"5","type":"tool.read","data":{"message":"no byte fields"}}`,
		`not json`,
	}, "\n")
	var out strings.Builder
	if err := replayJournal(strings.NewReader(journal), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// exec: 2 calls, 40100 raw bytes -> 10025 tok, 4100 -> 1025 tok, one call
	// under the inline threshold and one over.
	for _, want := range []string{"exec", "10025", "1025", "grep", "TOTAL", "of calls were small"} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "note") || strings.Contains(got, "read ") {
		t.Errorf("non-tool or byte-less events leaked into the report:\n%s", got)
	}
}

func TestReplayJournalEmpty(t *testing.T) {
	var out strings.Builder
	if err := replayJournal(strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no byte-counted") {
		t.Errorf("got %q", out.String())
	}
}
