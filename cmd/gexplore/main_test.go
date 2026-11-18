package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"graphify-explore/internal/chat"
	"graphify-explore/internal/graph"
)

func TestParseGraph(t *testing.T) {
	d := t.TempDir()
	if n, p := parseGraph("api=" + d + "/graph.json"); n != "api" || !strings.HasSuffix(p, "graph.json") {
		t.Fatal(n, p)
	}
	odd := filepath.Join(d, "a=b", "graphify-out", "graph.json")
	os.MkdirAll(filepath.Dir(odd), 0o755)
	os.WriteFile(odd, []byte("{}"), 0o600)
	if n, p := parseGraph(odd); n != "a=b" || p != odd {
		t.Fatal(n, p)
	}
	os.Chdir(filepath.Join(d, "a=b"))
	if n, _ := parseGraph("graphify-out/graph.json"); n == "." || n != "a=b" {
		t.Fatal(n)
	}
}

func TestWebHandler(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.json")
	os.WriteFile(p, []byte(`{"nodes":[{"id":"a","label":"S3Scanner","source_file":"s.go","source_location":"L5"}]}`), 0o600)
	g := graph.New()
	g.LoadFile("r", p)
	srv := newMux(&chat.Session{G: g, GH: "https://github.com/o/r/blob/main"}, chat.ContextOnly{}, "127.0.0.1:8080")
	do := func(method, host, hdr, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/ask", strings.NewReader(body))
		r.Host = host
		if hdr != "" {
			r.Header.Set("X-Gexplore", hdr)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "127.0.0.1:8080", "1", ""); w.Code != http.StatusForbidden {
		t.Fatal("GET allowed", w.Code)
	}
	if w := do("POST", "127.0.0.1:8080", "", "x"); w.Code != http.StatusForbidden {
		t.Fatal("no header allowed", w.Code)
	}
	if w := do("POST", "evil.example:8080", "1", "x"); w.Code != http.StatusForbidden {
		t.Fatal("bad host allowed", w.Code)
	}
	if w := do("POST", "127.0.0.1:8080", "1", strings.Repeat("a", maxQuestion+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversize allowed", w.Code)
	}
	w := do("POST", "localhost:8080", "1", "s3 scanner")
	b, _ := io.ReadAll(w.Body)
	if w.Code != 200 || !strings.Contains(string(b), "s.go:L5") || !strings.Contains(string(b), "Links:") || !strings.Contains(string(b), "```mermaid") {
		t.Fatalf("%d %s", w.Code, b)
	}
	_ = context.Background
	_ = time.Second
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=t"}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestStale(t *testing.T) {
	d := t.TempDir()
	git(t, d, "init", "-q")
	gp := filepath.Join(d, "graphify-out", "graph.json")
	os.MkdirAll(filepath.Dir(gp), 0o755)
	os.WriteFile(gp, []byte(`{"nodes":[]}`), 0o600)
	git(t, d, "commit", "-q", "--allow-empty", "-m", "x")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(gp, old, old)
	if st, err := stale(d, gp); err != nil || !st {
		t.Fatal("old graph should be stale", st, err)
	}
	now := time.Now().Add(time.Minute)
	os.Chtimes(gp, now, now)
	if st, _ := stale(d, gp); st {
		t.Fatal("fresh graph reported stale")
	}
	if st, err := stale(t.TempDir(), gp); st || err == nil {
		t.Fatal("non-repo must report error, not stale")
	}
	if err := maybeUpdate(gp); err != nil { // fresh: no graphify call
		t.Fatal(err)
	}
}
