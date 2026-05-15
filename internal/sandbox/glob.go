package sandbox

import (
	"container/list"
	"regexp"
	"strings"
	"sync"
)

func globMatch(pattern, text string, op string) bool {
	// Normalize Windows path separators for every path-style op so rules
	// written with forward slashes (e.g. "**/id_rsa", "**/.env*") match
	// `C:\Users\x\id_rsa` and `C:\proj\.env` regardless of which OS dfmt
	// is running on. F-03 closure: the previous version normalized only
	// the read op, so an agent on Windows could write through the deny
	// list because `**/.env*` did not match the backslash-separated
	// cleanPath. We use strings.ReplaceAll, NOT filepath.ToSlash, because
	// the latter is a no-op on Unix (where '\' is a valid filename byte)
	// and that lets a Windows-shaped path slip past the deny rules when
	// the daemon happens to run under WSL/Linux. Exec text is a shell
	// command (not a path) and must NOT be normalized — `git\branch` is a
	// literal-backslash arg and reparsing it would corrupt the matcher.
	if op != "exec" {
		text = strings.ReplaceAll(text, `\`, "/")
		pattern = strings.ReplaceAll(pattern, `\`, "/")
	}
	// For exec operations, use shell-style globbing where * matches anything including /
	// For path-style ops, * doesn't match / (path segments)
	if op == "exec" {
		// Lowercase text so deny rules written in conventional lowercase form
		// match uppercase invocations. Consistent with Rule.Match lowercasing.
		text = strings.ToLower(text)
		regex := globToRegexShell(pattern)
		return regexMatch(regex, text)
	}
	// Convert glob pattern to regex for path-style ops
	regex := globToRegex(pattern)
	return regexMatch(regex, text)
}

// globMatchDefault is for tests and direct calls that don't specify an operation.
// Uses path-style matching where * doesn't match / for backward compatibility.
func globMatchDefault(pattern, text string) bool {
	regex := globToRegex(pattern)
	return regexMatch(regex, text)
}

func globToRegexShell(pattern string) string {
	// Shell-style globbing: * matches anything including /
	// But /* means / followed by something (not just / alone)
	var result strings.Builder
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			// ** matches anything including path separators
			result.WriteString(".*")
			i += 2
		} else if i+1 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' {
			// /* means / followed by at least one character (like shell glob)
			if i+2 < len(pattern) && pattern[i+2] == '*' {
				// /** — matches zero or more path segments after /
				result.WriteString("/.*")
				i += 3
			} else {
				result.WriteString("/.+")
				i += 2
			}
		} else if pattern[i] == '*' {
			result.WriteString(".*") // * matches anything
			i++
		} else if pattern[i] == '?' {
			result.WriteByte('.')
			i++
		} else if pattern[i] == ' ' {
			// V-03: bash uses IFS (space, tab, newline) as a token separator,
			// but a rule like `deny:exec:sudo *` carries only a literal space.
			// Compile spaces in the pattern to `[ \t]+` so an agent that
			// substitutes a tab for the space (`sudo\twhoami`) doesn't slide
			// past the deny rule. Newline is excluded because the chain-aware
			// splitter (`hasShellChainOperators`) treats `\n` as a chain
			// boundary and recurses; a literal newline in a rule's first-arg
			// position would only ever be operator typo.
			result.WriteString("[ \\t]+")
			i++
		} else {
			// Escape regex metacharacters so user-authored patterns like
			// "api.example.com" don't have their dots interpreted as any-char
			// (silently broadening the match). The glob tokens we recognize
			// (* ** ? /) were already handled above.
			result.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	return "^" + result.String() + "$"
}

func globToRegex(pattern string) string {
	var result strings.Builder
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			// ** matches anything including path separators
			result.WriteString(".*")
			i += 2
		} else if pattern[i] == '*' {
			// Single * - for URL patterns like http://* or https://*, * must
			// match anything including /. The check i>=3 && pattern[i-3:i]
			// is `://` detects this and writes `.*` instead of `[^/]*`.
			if i >= 3 && pattern[i-3] == ':' && pattern[i-2] == '/' && pattern[i-1] == '/' {
				result.WriteString(".*")
			} else {
				result.WriteString("[^/]*")
			}
			i++
		} else if i+2 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' && pattern[i+2] == '*' {
			// /** — matches zero or more path segments after /.
			// Must check BEFORE the single /* branch since /* would match the
			// first * and leave the second star to be processed next.
			result.WriteString("/.*")
			i += 3
		} else if i+1 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' {
			// /* at end matches any non-empty path segment after /.
			// Skip if this is part of ://* URL pattern (://* should use .*)
			if i >= 2 && pattern[i-2] == ':' && pattern[i-1] == '/' {
				result.WriteByte(pattern[i])
				i++
			} else {
				result.WriteString("/[^/]+")
				i += 2
			}
		} else if pattern[i] == '?' {
			result.WriteByte('.')
			i++
		} else {
			// Escape regex metacharacters — same reasoning as in
			// globToRegexShell. Without this, a rule text like "a+b" fails
			// to compile or, worse, patterns with literal dots broaden
			// silently.
			result.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	return "^" + result.String() + "$"
}

// regexLRU is a small bounded LRU cache for compiled glob-derived regex patterns.
// Without a bound, loading a policy with thousands of unique rules would grow
// the cache indefinitely.
const regexLRUMaxEntries = 512

type regexLRUCache struct {
	mu      sync.Mutex
	order   *list.List
	entries map[string]*list.Element
}

type regexLRUEntry struct {
	key string
	re  *regexp.Regexp
}

var regexCache = &regexLRUCache{
	order:   list.New(),
	entries: make(map[string]*list.Element),
}

func (c *regexLRUCache) get(key string) (*regexp.Regexp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*regexLRUEntry).re, true
	}
	return nil, false
}

func (c *regexLRUCache) put(key string, re *regexp.Regexp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*regexLRUEntry).re = re
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&regexLRUEntry{key: key, re: re})
	c.entries[key] = el
	for c.order.Len() > regexLRUMaxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*regexLRUEntry).key)
	}
}

func regexMatch(pattern, text string) bool {
	if re, ok := regexCache.get(pattern); ok {
		return re.MatchString(text)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	regexCache.put(pattern, re)
	return re.MatchString(text)
}

// SandboxImpl is the default sandbox implementation.
