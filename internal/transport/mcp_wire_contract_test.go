package transport

import (
	"sort"
	"strings"
	"testing"
)

// TestMCPToolSchemaContract pins every tool's parameter names and required
// list — the part of tools/list an agent host builds its function calls
// from. Descriptions are free to change; a renamed, dropped or newly
// required parameter is a wire break and must show up here, deliberately
// (update this table in the same change, with a CHANGELOG entry). Issue #2
// (project_id) and the unadvertised fetch headers/body were drift of
// exactly this kind that no test noticed.
func TestMCPToolSchemaContract(t *testing.T) {
	want := map[string]struct{ params, required string }{
		"dfmt_exec":     {"async,cancel,code,env,intent,job_id,lang,return,timeout", ""},
		"dfmt_read":     {"intent,limit,offset,path,return", "path"},
		"dfmt_fetch":    {"body,headers,intent,method,return,timeout,url", "url"},
		"dfmt_remember": {"cached_tokens,input_tokens,message,model,output_tokens,tags,type", "type"},
		"dfmt_stats":    {"", ""},
		"dfmt_search":   {"limit,query,type", "query"},
		"dfmt_recall":   {"budget,format", ""},
		"dfmt_glob":     {"intent,pattern", "pattern"},
		"dfmt_grep":     {"case_insensitive,context,files,intent,path,pattern", "pattern"},
		"dfmt_edit":     {"new_string,old_string,path,replace_all", "path,old_string,new_string"},
		"dfmt_write":    {"content,path", "path,content"},
	}
	tools := mcpToolsBuiltin()
	if len(tools) != len(want) {
		t.Errorf("tools/list has %d tools, contract has %d", len(tools), len(want))
	}
	for _, tool := range tools {
		w, ok := want[tool.Name]
		if !ok {
			t.Errorf("tool %q is not in the contract", tool.Name)
			continue
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		names := make([]string, 0, len(props))
		for k := range props {
			names = append(names, k)
		}
		sort.Strings(names)
		req, _ := tool.InputSchema["required"].([]string)
		if got := strings.Join(names, ","); got != w.params {
			t.Errorf("%s params = %q, contract %q", tool.Name, got, w.params)
		}
		if got := strings.Join(req, ","); got != w.required {
			t.Errorf("%s required = %q, contract %q", tool.Name, got, w.required)
		}
	}
}
