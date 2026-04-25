package project

import "testing"

func TestIsDfmtIgnored(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"comment-only", "# only a comment\n", false},
		{"plain dir", ".dfmt/\n", true},
		{"plain no slash", ".dfmt\n", true},
		{"root anchored slash", "/.dfmt/\n", true},
		{"root anchored no slash", "/.dfmt\n", true},
		{"crlf line endings", ".dfmt/\r\nnode_modules\r\n", true},
		{"mixed entries", "node_modules\nbuild/\n.dfmt/\ndist\n", true},
		{"comment containing subject", "# we used to ignore .dfmt/ here\n", false},
		{"substring of unrelated path", "notes/.dfmt/sample\n", false},
		{"trailing-suffix not exact", "my.dfmt/\n", false},
		{"prefix not exact", ".dfmt-old/\n", false},
		{"whitespace before", "   .dfmt/   \n", true},
		{"negated last wins", ".dfmt/\n!.dfmt/\n", false},
		{"ignore re-added then removed", ".dfmt/\n!.dfmt/\n.dfmt/\n", true},
		{"only negation", "!.dfmt/\n", false},
		{"multiple lines no match", "build/\ndist/\n*.log\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDfmtIgnored([]byte(c.content)); got != c.want {
				t.Errorf("IsDfmtIgnored(%q) = %v, want %v", c.content, got, c.want)
			}
		})
	}
}
