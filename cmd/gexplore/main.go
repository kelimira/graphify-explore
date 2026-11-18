// gexplore: conversational Q&A over Graphify graphs (CLI + web).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"graphify-explore/internal/chat"
	"graphify-explore/internal/graph"
)

const page = `<!doctype html><meta charset=utf-8><title>gexplore</title>
<pre id=log style="white-space:pre-wrap"></pre><div id=dia></div><input id=q size=80 autofocus>
<script src="https://cdn.jsdelivr.net/npm/mermaid@10.9.3/dist/mermaid.min.js" integrity="sha384-R63zfMfSwJF4xCR11wXii+QUsbiBIdiDzDbtxia72oGWfkT7WHJfmD/I/eeHPJyT" crossorigin=anonymous></script>
<script>
let n=0;
q.onkeydown=async e=>{if(e.key!='Enter')return;const v=q.value;q.value='';log.textContent+='\n> '+v+'\n';
const r=await fetch('/ask',{method:'POST',headers:{'X-Gexplore':'1'},body:v});const d=r.body.getReader();const t=new TextDecoder();let all='';
for(;;){const {done,value}=await d.read();if(done)break;const c=t.decode(value);all+=c;log.textContent+=c}
const m=all.match(/\x60\x60\x60mermaid\n([\s\S]*?)\x60\x60\x60/);
if(m){try{const o=await mermaid.render('d'+(n++),m[1]);dia.innerHTML=o.svg}catch(x){}}}
</script>`

func main() {
	var repos multi
	flag.Var(&repos, "graph", "[name=]path/to/graph.json (repeatable; several = cross-repo mesh)")
	web := flag.String("web", "", "serve web UI on this addr, e.g. 127.0.0.1:8080 (a bare :port binds loopback)")
	llm := flag.String("llm", "", `LLM CLI that reads the prompt on stdin, e.g. "claude -p"; empty = print graph context only`)
	update := flag.Bool("update", false, "run `graphify update` for each graph's repo when the graph is older than git HEAD")
	root := flag.String("root", "", "local checkout root for vscode:// links")
	gh := flag.String("github", "", "GitHub blob base for links, e.g. https://github.com/o/r/blob/main")
	flag.Parse()
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gexplore -graph name=graphify-out/graph.json [-web 127.0.0.1:8080] [-llm 'claude -p']")
		os.Exit(2)
	}
	g := graph.New()
	for _, r := range repos {
		name, path := parseGraph(r)
		if *update {
			if err := maybeUpdate(path); err != nil {
				fmt.Fprintln(os.Stderr, "update:", err)
			}
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
	s := &chat.Session{G: g, Root: *root, GH: *gh}
	if *web == "" {
		chat.REPL(context.Background(), s, run, os.Stdin, os.Stdout)
		return
	}
	addr := *web
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	fmt.Fprintln(os.Stderr, "listening on", addr)
	if err := http.ListenAndServe(addr, newMux(s, run, addr)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseGraph splits "name=path". An existing file wins, so paths containing "=" work;
// without a name, it is the directory above graphify-out (absolute, never ".").
func parseGraph(r string) (name, path string) {
	if _, err := os.Stat(r); err != nil {
		if n, p, ok := strings.Cut(r, "="); ok {
			return n, p
		}
	}
	abs, err := filepath.Abs(r)
	if err != nil {
		abs = r
	}
	return filepath.Base(filepath.Dir(filepath.Dir(abs))), r
}

const maxQuestion = 4096

// newMux serves the UI and /ask. Defences for a local tool: Host allow-list (DNS
// rebinding), POST + custom header (cross-site requests cannot send it without a
// preflight we never grant), body cap, and one question at a time (token cost, session memory).
func newMux(s *chat.Session, run chat.Runner, addr string) http.Handler {
	host, _, _ := net.SplitHostPort(addr)
	okHost := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, host: host != ""}
	guard := func(w http.ResponseWriter, r *http.Request) bool {
		h, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			h = strings.Trim(r.Host, "[]")
		}
		if !okHost[h] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return false
		}
		return true
	}
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r) {
			fmt.Fprint(w, page)
		}
	})
	mux.HandleFunc("/ask", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("X-Gexplore") != "1" {
			http.Error(w, "POST with X-Gexplore header required", http.StatusForbidden)
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxQuestion))
		if err != nil {
			http.Error(w, "question too large", http.StatusRequestEntityTooLarge)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		c := s.Ask(string(b))
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		var buf strings.Builder
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := run.Run(ctx, chat.Prompt(c), io.MultiWriter(flushWriter{w, fl}, &buf)); err != nil {
			fmt.Fprintf(w, "\nerror: %v\n", err)
		}
		chat.Finish(w, s.G, c, buf.String())
	})
	return mux
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

// stale reports whether graphPath is older than the repo's HEAD commit (or its recorded commit differs).
func stale(root, graphPath string) (bool, error) {
	out, err := exec.Command("git", "-C", root, "log", "-1", "--format=%H %ct").Output()
	if err != nil {
		return false, err // not a git repo: nothing to compare
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return false, fmt.Errorf("unexpected git output %q", out)
	}
	// shortcut: mtime only (graphify graphs carry no commit); avoids parsing the whole graph twice.
	ct, _ := strconv.ParseInt(f[1], 10, 64)
	fi, err := os.Stat(graphPath)
	if err != nil {
		return false, err
	}
	return fi.ModTime().Unix() < ct, nil
}

// maybeUpdate runs `graphify update` in the repo root when the graph is stale.
// shortcut: runs once at startup, not as a background watcher; upgrade to a poll loop if needed.
func maybeUpdate(graphPath string) error {
	root := filepath.Dir(filepath.Dir(graphPath))
	if st, err := stale(root, graphPath); err != nil || !st {
		return nil
	}
	cmd := exec.Command("graphify", "update", root)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	// touch so an update that changed nothing is not re-run on every start
	now := time.Now()
	return os.Chtimes(graphPath, now, now)
}
