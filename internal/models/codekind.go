package models

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// Values of the code_kind filter.
const (
	CodeKindTest       = "test"
	CodeKindProduction = "production"
)

// testPathPatterns are the common conventions for test files across
// languages, from which a Code document's code_kind is derived unless its
// source overrides them (ConfigTestFilePatterns).
var testPathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|/)(tests?|__tests__|spec|specs)/`),           // tests/, test/, __tests__/, spec/
	regexp.MustCompile(`(^|/)src/test/`),                                // Maven / Gradle
	regexp.MustCompile(`_test\.(go|py|rb|exs?)$`),                       // Go, Python, Ruby, Elixir
	regexp.MustCompile(`(^|/)test_[^/]+\.py$`),                          // pytest
	regexp.MustCompile(`\.(test|spec)\.(t|j)sx?$`),                      // JS / TS
	regexp.MustCompile(`(Tests?|Spec|IT)\.(cs|java|kt|scala|swift|m)$`), // C#, Java, Kotlin, Swift, ObjC
	regexp.MustCompile(`\.Tests?/`),                                     // .NET test projects
}

func cleanRepoPath(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
}

// IsTestPath reports whether p looks like a test file by common conventions.
func IsTestPath(p string) bool {
	p = cleanRepoPath(p)
	return slices.ContainsFunc(testPathPatterns, func(re *regexp.Regexp) bool { return re.MatchString(p) })
}

// ParseTestPatterns splits a "Test file patterns" setting (comma- or
// newline-separated wildcards) into its patterns.
func ParseTestPatterns(raw string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// MatchPathPattern reports whether repository path p matches wildcard
// pattern: "*" matches within one folder, "**" across folders, "?" one
// character. A pattern without a "/" matches the file name in any folder
// (e.g. *_test.go); one with a "/" matches the path from the repository
// root (e.g. tests/**). Matching is case-insensitive.
func MatchPathPattern(pattern, p string) bool {
	return compilePathPattern(pattern)(p)
}

// compilePathPattern compiles one wildcard for MatchPathPattern.
func compilePathPattern(pattern string) func(string) bool {
	pattern = strings.ToLower(cleanRepoPath(pattern))
	if pattern == "" {
		return func(string) bool { return false }
	}
	re := globRegexp(pattern)
	if !strings.Contains(pattern, "/") && pattern != "**" {
		return func(p string) bool { return re.MatchString(path.Base(strings.ToLower(cleanRepoPath(p)))) }
	}
	return func(p string) bool { return re.MatchString(strings.ToLower(cleanRepoPath(p))) }
}

func globRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			i++
			if i+1 < len(pattern) && pattern[i+1] == '/' {
				i++
				b.WriteString("(.*/)?") // "**/" matches zero or more folders
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// CodeKindFunc returns the classifier of Code files: "test" for a path
// matching any of patterns, or, with no patterns, the common conventions
// (IsTestPath); otherwise "production". Patterns are compiled once.
func CodeKindFunc(patterns []string) func(p string) string {
	matchers := make([]func(string) bool, 0, len(patterns))
	for _, pat := range patterns {
		matchers = append(matchers, compilePathPattern(pat))
	}
	return func(p string) string {
		isTest := false
		if len(matchers) == 0 {
			isTest = IsTestPath(p)
		} else {
			isTest = slices.ContainsFunc(matchers, func(m func(string) bool) bool { return m(p) })
		}
		if isTest {
			return CodeKindTest
		}
		return CodeKindProduction
	}
}

// CodeKind classifies one path; see CodeKindFunc.
func CodeKind(p string, patterns []string) string { return CodeKindFunc(patterns)(p) }
