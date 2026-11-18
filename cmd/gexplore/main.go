// gexplore: conversational Q&A over Graphify graphs (CLI + web).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"graphify-explore/internal/chat"
	"graphify-explore/internal/graph"
)

const page = `<!doctype html><meta charset=utf-8><title>gexplore</title>
<pre id=log style="white-space:pre-wrap"></pre><input id=q size=80 autofocus>
<script>
q.onkeydown=async e=>{if(e.key!='Enter')return;const v=q.value;q.value='';log.textContent+='\n> '+v+'\n';
const r=await fetch('/ask?q='+encodeURIComponent(v));const d=r.body.getReader();const t=new TextDecoder();
for(;;){const {done,value}=await d.read();if(done)break;log.textContent+=t.decode(value)}}
</script>`

func main() {
	var repos multi
	flag.Var(&repos, "graph", "name=path/to/graph.json (repeatable; several = cross-repo mesh)")
	web := flag.String("web", "", "serve web UI on this addr, e.g. :8080")
	llm := flag.String("llm", "", `LLM CLI that reads the prompt on stdin, e.g. "claude -p"; empty = print graph context only`)
	update := flag.Bool("update", false, "run `graphify update` in each graph's repo when its commit is stale")
	flag.Parse()
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gexplore -graph name=graphify-out/graph.json [-web :8080] [-llm 'claude -p']")
		os.Exit(2)
	}
	g := graph.New()
	for _, r := range repos {
		name, path, _ := strings.Cut(r, "=")
		if path == "" {
			name, path = filepath.Base(filepath.Dir(filepath.Dir(r))), r
		}
		if *update {
			maybeUpdate(path)
		}
		if err := g.LoadFile(name, path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	var run chat.Runner = chat.ContextOnly{}
	if f := strings.Fields(*llm); len(f) > 0 {
		run = chat.CmdRunner{Argv: f}
	}
	s := &chat.Session{G: g}
	if *web == "" {
		chat.REPL(context.Background(), s, run, os.Stdin, os.Stdout)
		return
	}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, page) })
	var mu sync.Mutex // Session memory is not goroutine-safe
	http.HandleFunc("/ask", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := s.Ask(r.URL.Query().Get("q"))
		mu.Unlock()
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_ = run.Run(r.Context(), chat.Prompt(c), flushWriter{w, fl})
		fmt.Fprintf(w, "\n\n%s\n", c.Mermaid)
	})
	fmt.Fprintln(os.Stderr, "listening on", *web)
	if err := http.ListenAndServe(*web, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type flushWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.fl != nil {
		f.fl.Flush()
	}
	return n, err
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// maybeUpdate runs `graphify update` in the repo root when HEAD moved past the graph's commit.
// shortcut: runs once at startup, not as a background watcher; upgrade to a fsnotify/poll loop if needed.
func maybeUpdate(graphPath string) {
	root := filepath.Dir(filepath.Dir(graphPath))
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return
	}
	if g := graph.New(); g.LoadFile("x", graphPath) == nil && strings.TrimSpace(string(head)) == g.Commits["x"] {
		return
	}
	cmd := exec.Command("graphify", "update", root)
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
