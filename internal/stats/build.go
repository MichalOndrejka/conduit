// Package stats records what Conduit does over time (searches the agent
// makes, sync runs) to an append-only file in the data directory, and
// summarizes it per build, so versions can be compared: did a change make
// searches come back empty more often, or a sync slower or more expensive?
//
// Every event is written to disk as it happens and nothing is aggregated in
// memory, because a development build may only live for minutes (hot
// reload, frequent restarts); the file is the history and survives them.
package stats

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// Build identifies the code a Conduit process runs. Restarting the same
// code gives the same ID; any committed or uncommitted change gives a new
// one, so each version's stats stay together and apart from the others.
type Build struct {
	// ID is the short commit, plus "+" and a hash of the uncommitted
	// changes when the working tree differs from it.
	ID         string    `json:"id"`
	Commit     string    `json:"commit,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	CommitTime time.Time `json:"commit_time,omitzero"`
	Dirty      bool      `json:"dirty,omitempty"`
	Version    string    `json:"version,omitempty"`
}

// Label is a short human description, e.g. "a1b2c3d4e5 + changes: Add tarball fetch".
func (b Build) Label() string {
	id := b.ID
	if b.Dirty && len(b.Commit) > 0 {
		id = b.Commit + " + changes"
	}
	if b.Subject != "" {
		return id + ": " + b.Subject
	}
	return id
}

// DetectBuild identifies the running code from the git checkout in the
// working directory (go run, air), falling back to the VCS stamp go build
// embeds, then to version.
func DetectBuild(version string) Build {
	b := Build{Version: version}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	git := func(args ...string) (string, bool) {
		out, err := exec.CommandContext(ctx, "git", args...).Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	if commit, ok := git("rev-parse", "--short=10", "HEAD"); ok && commit != "" {
		b.Commit = commit
		if info, ok := git("log", "-1", "--format=%ct%n%s"); ok {
			if ts, subject, found := strings.Cut(info, "\n"); found {
				var secs int64
				if _, err := fmt.Sscan(ts, &secs); err == nil {
					b.CommitTime = time.Unix(secs, 0).UTC()
				}
				b.Subject = subject
			}
		}
		b.ID = commit
		if h := worktreeHash(ctx); h != "" {
			b.Dirty = true
			b.ID = commit + "+" + h
		}
		return b
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value[:min(10, len(s.Value))]
			case "vcs.modified":
				b.Dirty = s.Value == "true"
			case "vcs.time":
				b.CommitTime, _ = time.Parse(time.RFC3339, s.Value)
			}
		}
	}
	switch {
	case b.Commit != "" && b.Dirty:
		// The stamp says "modified" but not how: every such binary is its own
		// build, identified by its own content.
		b.ID = b.Commit + "+" + executableHash()
	case b.Commit != "":
		b.ID = b.Commit
	default:
		b.ID = "v" + version
	}
	return b
}

// worktreeHash hashes the uncommitted changes (tracked diffs and the content
// of untracked files), or returns "" for a clean tree.
func worktreeHash(ctx context.Context) string {
	diff, err := exec.CommandContext(ctx, "git", "diff", "HEAD", "--no-color", "--no-ext-diff").Output()
	if err != nil {
		return ""
	}
	untracked, _ := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z").Output()
	// Only new source files change what runs; untracked tool folders
	// (virtualenvs, caches, editor state) must not.
	var names []string
	for _, name := range strings.Split(string(untracked), "\x00") {
		if name != "" && !strings.HasPrefix(name, ".") && !strings.Contains(name, "/.") &&
			sourceExt[strings.ToLower(filepathExt(name))] {
			names = append(names, name)
		}
	}
	if len(bytes.TrimSpace(diff)) == 0 && len(names) == 0 {
		return ""
	}
	h := fnv.New64a()
	h.Write(diff)
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte(name))
		if fi, err := os.Stat(name); err == nil && fi.Size() <= 1<<20 {
			if data, err := os.ReadFile(name); err == nil {
				h.Write(data)
			}
		}
	}
	return fmt.Sprintf("%08x", uint32(h.Sum64()))
}

// sourceExt are the file types whose untracked additions count as a code
// change.
var sourceExt = map[string]bool{
	".go": true, ".html": true, ".css": true, ".js": true, ".ts": true,
	".mod": true, ".sum": true, ".tmpl": true, ".sql": true, ".py": true,
}

func filepathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > strings.LastIndexByte(name, '/') {
		return name[i:]
	}
	return ""
}

func executableHash() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	h := fnv.New32a()
	h.Write(data)
	return fmt.Sprintf("%08x", h.Sum32())
}
