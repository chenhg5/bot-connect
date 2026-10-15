// Package probe runs observe.Probes on this machine: reads files and
// directories inside allowed roots, runs allowed read-only commands (no
// shell), fetches allowed hosts. Output is capped and secrets are masked.
package probe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// DefaultCommands are read-only subcommands of common tools. "*" matches
// one argument that isn't a flag. Anything else is added by configuration
// or, at run time, by the principal.
var DefaultCommands = []string{
	"git status", "git log", "git show", "git diff", "git branch", "git tag", "git rev-parse", "git ls-files", "git shortlog", "git describe",
	"gh pr list", "gh pr view", "gh pr checks", "gh pr status", "gh pr diff",
	"gh issue list", "gh issue view", "gh issue status",
	"gh release list", "gh release view", "gh run list", "gh run view", "gh repo view", "gh search prs", "gh search issues",
}

// deniedFlags could write files, change configuration or run programs.
var deniedFlags = []string{"--output", "-o", "--exec", "--config", "-c", "--upload-pack", "--receive-pack", "--git-dir", "--work-tree",
	"-C", "--web", "-w", "--editor", "--ext-diff", "--textconv", "--open-files-in-pager"}

// Local runs probes on this machine.
type Local struct {
	Roots    []string // absolute; files, directories and command working directories must be inside one
	Commands []string // allowed command patterns (DefaultCommands when nil)
	Hosts    []string // hosts http probes may fetch
	Timeout  time.Duration
	MaxBytes int
	Extra    func() []string // command patterns allowed at run time
}

func (l *Local) timeout() time.Duration {
	if l.Timeout <= 0 {
		return 20 * time.Second
	}
	return l.Timeout
}

func (l *Local) maxBytes() int {
	if l.MaxBytes <= 0 {
		return 32 << 10
	}
	return l.MaxBytes
}

// Probe looks and returns what it saw (capped, secrets masked).
func (l *Local) Probe(ctx context.Context, p observe.Probe) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, l.timeout())
	defer cancel()
	var out string
	var err error
	switch p.Kind {
	case observe.File:
		out, err = l.file(p.Target)
	case observe.Dir:
		out, err = l.dir(p.Target, p.Depth)
	case observe.Cmd:
		out, err = l.cmd(ctx, p)
	case observe.HTTP:
		out, err = l.http(ctx, p.Target)
	}
	return l.clip(Redact(out)), err
}

func (l *Local) clip(s string) string {
	if len(s) <= l.maxBytes() {
		return s
	}
	cut := l.maxBytes()
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n…（截断，共 %d 字节）", len(s))
}

// Inside resolves a path and checks it is inside an allowed root.
func (l *Local) Inside(path string) (string, error) {
	if len(l.Roots) == 0 {
		return "", Forbidden("no directories are open to look at ([observe] roots)")
	}
	abs, err := filepath.Abs(expand(path))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", NotFound("%s: %v", path, err)
	}
	for _, r := range l.Roots {
		rr, err := filepath.EvalSymlinks(expand(r))
		if err != nil {
			continue
		}
		if real == rr || strings.HasPrefix(real, rr+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", Forbidden("%s is outside the directories open to look at (%s)", path, strings.Join(l.Roots, ", "))
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func (l *Local) file(path string) (string, error) {
	real, err := l.Inside(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", Invalid("%s is a directory: use a dir probe", path)
	}
	f, err := os.Open(real)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(l.maxBytes())+1))
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
		return "", Invalid("%s doesn't look like text", path)
	}
	return string(b), nil
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "__pycache__": true, "vendor": true, "dist": true, "build": true}

// dir lists a tree: relative path, size, modification time; the newest
// files first in a summary line, which is what "is anything moving?" needs.
func (l *Local) dir(path string, depth int) (string, error) {
	real, err := l.Inside(path)
	if err != nil {
		return "", err
	}
	if depth <= 0 {
		depth = 2
	}
	depth = min(depth, 4)
	type entry struct {
		rel  string
		size int64
		mod  time.Time
		dir  bool
	}
	var es []entry
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == real {
			return nil
		}
		rel, _ := filepath.Rel(real, p)
		if d.IsDir() && (skipDirs[d.Name()] || strings.Count(rel, string(filepath.Separator)) >= depth) {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			es = append(es, entry{rel: rel + "/", dir: true})
			return filepath.SkipDir
		}
		if len(es) >= 600 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		es = append(es, entry{rel: rel, size: info.Size(), mod: info.ModTime(), dir: d.IsDir()})
		return nil
	})
	if err != nil {
		return "", err
	}
	var files []entry
	for _, e := range es {
		if !e.dir {
			files = append(files, e)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	var b strings.Builder
	fmt.Fprintf(&b, "%s：%d 项", path, len(es))
	if len(files) > 0 {
		b.WriteString("；最近改动：")
		for i, f := range files {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "%s（%s）", f.rel, f.mod.Format("01-02 15:04"))
			if i < min(len(files), 5)-1 {
				b.WriteString("、")
			}
		}
	}
	b.WriteString("\n")
	sort.Slice(es, func(i, j int) bool { return es[i].rel < es[j].rel })
	for _, e := range es {
		if e.dir {
			fmt.Fprintf(&b, "%s\n", e.rel)
		} else {
			fmt.Fprintf(&b, "%s  %dB  %s\n", e.rel, e.size, e.mod.Format("2006-01-02 15:04"))
		}
	}
	return b.String(), nil
}

// Allowed reports whether a command (program + args) matches an allowed
// pattern and uses no dangerous flag.
func (l *Local) Allowed(program string, args []string) error {
	if strings.ContainsAny(program, `/\`) {
		return Forbidden("give the program by name, not by path")
	}
	for _, a := range args {
		for _, d := range deniedFlags {
			if a == d || strings.HasPrefix(a, d+"=") || (len(d) > 2 && strings.HasPrefix(a, d)) {
				return Forbidden("flag %s is not allowed in a probe (it could write or run something)", a)
			}
		}
	}
	pats := l.Commands
	if pats == nil {
		pats = DefaultCommands
	}
	if l.Extra != nil {
		pats = append(append([]string(nil), pats...), l.Extra()...)
	}
	argv := append([]string{program}, args...)
	for _, p := range pats {
		if matches(strings.Fields(p), argv) {
			return nil
		}
	}
	return Forbidden("%q is not an allowed read-only command; the principal can allow it (observe_allow)", strings.Join(argv, " "))
}

func matches(pattern, argv []string) bool {
	if len(argv) < len(pattern) {
		return false
	}
	for i, t := range pattern {
		if t == "*" {
			if strings.HasPrefix(argv[i], "-") {
				return false
			}
			continue
		}
		if t != argv[i] {
			return false
		}
	}
	return true
}

func (l *Local) cmd(ctx context.Context, p observe.Probe) (string, error) {
	if err := l.Allowed(p.Target, p.Args); err != nil {
		return "", err
	}
	dir := p.Dir
	if dir == "" && len(l.Roots) > 0 {
		dir = l.Roots[0]
	}
	real, err := l.Inside(dir)
	if err != nil {
		return "", err
	}
	bin, err := exec.LookPath(p.Target)
	if err != nil {
		return "", NotFound("%s is not installed", p.Target)
	}
	c := exec.CommandContext(ctx, bin, p.Args...)
	c.Dir = real
	c.Env = append(os.Environ(), "PAGER=cat", "GIT_PAGER=cat", "GH_PAGER=cat", "NO_COLOR=1", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "TERM=dumb")
	c.Stdin = nil
	var out strings.Builder
	c.Stdout, c.Stderr = &limited{w: &out, n: l.maxBytes() + 1}, &limited{w: &out, n: l.maxBytes() + 1}
	err = c.Run()
	if ctx.Err() != nil {
		return out.String(), fmt.Errorf("timed out after %s", l.timeout())
	}
	if err != nil {
		return out.String(), fmt.Errorf("%s: %v: %s", p, err, strings.TrimSpace(Redact(out.String())))
	}
	return out.String(), nil
}

type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	k := min(len(p), l.n)
	l.n -= k
	_, err := l.w.Write(p[:k])
	return len(p), err
}

func (l *Local) http(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", Invalid("not an http(s) URL: %s", raw)
	}
	ok := false
	for _, h := range l.Hosts {
		ok = ok || strings.EqualFold(u.Hostname(), h) || strings.HasSuffix(strings.ToLower(u.Hostname()), "."+strings.ToLower(h))
	}
	if !ok {
		return "", Forbidden("host %s is not open to look at ([observe] hosts)", u.Hostname())
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	req.Header.Set("User-Agent", "bot-connect-observe")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(l.maxBytes())+1))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return string(b), fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}

// secret-looking things never leave a probe.
var secrets = []*regexp.Regexp{
	regexp.MustCompile(`apikey_[A-Za-z0-9_]{8,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{5,}`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]{16,}`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|secret|token|password|passwd|app_secret)\s*[=:]\s*["']?)[^"'\s]{6,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// Redact masks secrets.
func Redact(s string) string {
	for _, re := range secrets {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			if len(sub) > 1 && sub[1] != "" && strings.HasPrefix(m, sub[1]) {
				return sub[1] + "***"
			}
			return "***"
		})
	}
	return s
}
