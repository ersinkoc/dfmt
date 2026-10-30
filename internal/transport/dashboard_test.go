package transport

import (
	"regexp"
	"strings"
	"testing"
)

// Every element id the dashboard script looks up must exist in the markup;
// a typo otherwise surfaces as a null dereference in the browser only.
func TestDashboardJSIDsExistInHTML(t *testing.T) {
	re := regexp.MustCompile(`getElementById\('([^']+)'\)`)
	ids := re.FindAllStringSubmatch(DashboardJS, -1)
	if len(ids) == 0 {
		t.Fatal("no getElementById calls found")
	}
	for _, m := range ids {
		if !strings.Contains(DashboardHTML, `id="`+m[1]+`"`) {
			t.Errorf("dashboard.js reads #%s, which is not in the HTML", m[1])
		}
	}
	if !strings.Contains(DashboardHTML, `id="tool-table"`) {
		t.Error("per-tool ledger table missing")
	}
}

func TestCloneStatsResponseCopiesToolBytes(t *testing.T) {
	src := &StatsResponse{ToolBytes: map[string]ToolByteStats{"tool.exec": {Calls: 1}}}
	cp := cloneStatsResponse(src)
	cp.ToolBytes["tool.exec"] = ToolByteStats{Calls: 99}
	cp.ToolBytes["tool.read"] = ToolByteStats{Calls: 1}
	if src.ToolBytes["tool.exec"].Calls != 1 || len(src.ToolBytes) != 1 {
		t.Errorf("clone shares ToolBytes with the cached response: %+v", src.ToolBytes)
	}
}
