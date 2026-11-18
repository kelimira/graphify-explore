package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"graphify-explore/internal/graph"
)

func load(t *testing.T) *graph.Graph {
	body := `{"nodes":[
{"id":"w","label":"JobWorker","source_file":"src/jobs/worker.rs","source_location":"L78","community":1,"community_name":"scanning"},
{"id":"s","label":"S3BucketScanner","source_file":"src/scan/s3.rs","source_location":"L10","community":1,"community_name":"scanning"},
{"id":"e","label":"handle_error","source_file":"src/scan/err.rs","source_location":"L5","community":1,"community_name":"scanning"}],
"links":[{"source":"w","target":"s","relation":"calls"},{"source":"s","target":"e","relation":"calls"}]}`
	p := filepath.Join(t.TempDir(), "graph.json")
	os.WriteFile(p, []byte(body), 0o600)
	g := graph.New()
	if err := g.LoadFile("api", p); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAskFollowUpUsesPreviousSlice(t *testing.T) {
	s := &Session{G: load(t)}
	c := s.Ask("what scans s3 buckets?")
	if !strings.Contains(c.Text, "src/scan/s3.rs:L10") || !strings.Contains(c.Mermaid, "-->|calls|") {
		t.Fatalf("bad context:\n%s\n%s", c.Text, c.Mermaid)
	}
	c = s.Ask("show me that one") // no keyword hits -> memory
	if !strings.Contains(c.Text, "handle_error") {
		t.Fatalf("follow-up lost memory:\n%s", c.Text)
	}
}

func TestTokenBudget(t *testing.T) {
	g := graph.New()
	var nodes, links []string
	for i := 0; i < 200; i++ {
		nodes = append(nodes, fmt.Sprintf(`{"id":"n%d","label":"ScanVeryLongDescriptiveFunctionName%d","source_file":"internal/some/deep/package/path/file%d.go","source_location":"L%d","community":1}`, i, i, i, i+1))
		if i > 0 {
			links = append(links, fmt.Sprintf(`{"source":"n0","target":"n%d","relation":"calls"}`, i))
		}
	}
	p := filepath.Join(t.TempDir(), "g.json")
	os.WriteFile(p, []byte(`{"nodes":[`+strings.Join(nodes, ",")+`],"links":[`+strings.Join(links, ",")+`]}`), 0o600)
	if err := g.LoadFile("r", p); err != nil {
		t.Fatal(err)
	}
	c := (&Session{G: g}).Ask("scan")
	if c.Tokens > TokenBudget || len(c.Slice.Nodes) > MaxNodes {
		t.Fatalf("tokens=%d nodes=%d", c.Tokens, len(c.Slice.Nodes))
	}
}

func TestVerifyCitations(t *testing.T) {
	ok, bad := Verify(load(t), "Worker calls scanner (src/jobs/worker.rs:L78) and fake (src/x.rs:L9, src/scan/s3.rs:L11).")
	if len(ok) != 1 || len(bad) != 2 {
		t.Fatalf("ok=%v bad=%v", ok, bad)
	}
}

func TestLinks(t *testing.T) {
	n := &graph.Node{File: "src/a.go", Loc: "L7"}
	if l := Link(n, "/repo", ""); l != "vscode://file//repo/src/a.go:7" && l != "vscode://file/repo/src/a.go:7" {
		t.Fatal(l)
	}
	if l := Link(n, "", "https://github.com/o/r/blob/main/"); l != "https://github.com/o/r/blob/main/src/a.go#L7" {
		t.Fatal(l)
	}
}

func TestREPLWithContextOnlyAndCmdRunner(t *testing.T) {
	s := &Session{G: load(t)}
	var out strings.Builder
	REPL(context.Background(), s, ContextOnly{}, strings.NewReader("s3 scanner\nexit\n"), &out)
	if !strings.Contains(out.String(), "```mermaid") {
		t.Fatal(out.String())
	}
	out.Reset()
	// the LLM CLI echoes a fabricated citation; REPL must flag it
	REPL(context.Background(), s, CmdRunner{Argv: []string{"sh", "-c", "echo bogus.go:L1"}}, strings.NewReader("s3\n"), &out)
	if !strings.Contains(out.String(), "unverified citations: bogus.go:L1") {
		t.Fatal(out.String())
	}
}

func TestFollowUpWithKeywordsStaysInPreviousSlice(t *testing.T) {
	s := &Session{G: load(t)}
	s.Ask("what scans s3 buckets?")
	c := s.Ask("Show me the error handling in that scanner")
	if !strings.Contains(c.Text, "handle_error") || !strings.Contains(c.Text, "S3BucketScanner") {
		t.Fatalf("follow-up ignored memory:\n%s", c.Text)
	}
}

func TestVerifyRanges(t *testing.T) {
	g := load(t)
	ok, bad := Verify(g, "see src/jobs/worker.rs:L78-90 and src/jobs/worker.rs:L99")
	if len(ok) != 1 || len(bad) != 1 || bad[0] != "src/jobs/worker.rs:L99" {
		t.Fatalf("ok=%v bad=%v", ok, bad)
	}
}

func TestMermaidInjection(t *testing.T) {
	g := graph.New()
	p := filepath.Join(t.TempDir(), "g.json")
	os.WriteFile(p, []byte(`{"nodes":[{"id":"a","label":"x\"]-->evil[\"y","source_file":"a.go"},{"id":"b","label":"b"}],"links":[{"source":"a","target":"b","relation":"c]a\"lls"}]}`), 0o600)
	g.LoadFile("r", p)
	m := Mermaid(g, g.Expand([]int{0}, 5))
	if strings.Contains(m, `\"`) || strings.Count(m, `"`) != 4 || strings.Contains(m, "c]a") {
		t.Fatalf("unsafe mermaid:\n%s", m)
	}
}
