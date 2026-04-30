package redact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadResult carries the override-file state for a single LoadProjectRedactor
// call. Mirrors sandbox.PolicyLoadResult so callers (daemon startup,
// dfmt doctor) can render a uniform "override file" row.
//
// See ADR-0014 for the wiring decision.
type LoadResult struct {
	OverridePath   string   // absolute path checked
	OverrideFound  bool     // file existed and was parsed
	PatternsLoaded int      // count of patterns successfully added
	Warnings       []string // per-entry parse / regex / missing-field issues
}

// loadFile is the on-disk YAML shape. Kept private — callers consume
// LoadResult, not the raw struct.
type loadFile struct {
	Patterns []struct {
		Name        string `yaml:"name"`
		Pattern     string `yaml:"pattern"`
		Replacement string `yaml:"replacement"`
	} `yaml:"patterns"`
}

// LoadProjectRedactor returns a Redactor seeded with the default common
// patterns, then augmented with any operator-defined patterns from
// `<projectPath>/.dfmt/redact.yaml`. A missing override file is not an
// error — the returned Redactor is identical to NewRedactor().
//
// File format:
//
//	patterns:
//	  - name: company-api-key
//	    pattern: 'CO-[A-Z0-9]{20}'
//	    replacement: '[CO-KEY]'   # optional; defaults to "[REDACTED-<NAME>]"
//	  - name: internal-id
//	    pattern: 'INT-\d{6,}'
//
// An invalid regex or a missing required field on a single entry is
// reported in LoadResult.Warnings and skipped — one bad pattern does not
// kill the whole load. A YAML parse error or read error on the file as a
// whole is returned to the caller.
func LoadProjectRedactor(projectPath string) (*Redactor, LoadResult, error) {
	r := NewRedactor()
	var res LoadResult
	if projectPath == "" {
		return r, res, nil
	}
	overridePath := filepath.Join(projectPath, ".dfmt", "redact.yaml")
	res.OverridePath = overridePath

	data, err := os.ReadFile(overridePath)
	if err != nil {
		if os.IsNotExist(err) {
			return r, res, nil
		}
		return r, res, fmt.Errorf("read %s: %w", overridePath, err)
	}

	var ff loadFile
	if err := yaml.Unmarshal(data, &ff); err != nil {
		return r, res, fmt.Errorf("parse %s: %w", overridePath, err)
	}
	res.OverrideFound = true

	for i, p := range ff.Patterns {
		if p.Name == "" || p.Pattern == "" {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("entry %d: 'name' and 'pattern' are required", i+1))
			continue
		}
		repl := p.Replacement
		if repl == "" {
			repl = "[REDACTED-" + strings.ToUpper(p.Name) + "]"
		}
		if err := r.AddPattern(p.Name, p.Pattern, repl); err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("pattern %q: %v", p.Name, err))
			continue
		}
		res.PatternsLoaded++
	}
	return r, res, nil
}
