package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, body string) string {
	p := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const backend = `{"built_at_commit":"abc","nodes":[
{"id":"worker","label":"JobWorker::process_tenant_scan_job","source_file":"src/jobs/worker.rs","source_location":"L78","community":1,"community_name":"scanning"},
{"id":"s3","label":"S3BucketScanner","source_file":"src/scan/s3.rs","source_location":"L10","community":1,"community_name":"scanning"},
{"id":"list","label":"ListObjectsV2","source_file":"src/scan/s3.rs","source_location":"L42","community":1,"community_name":"scanning"},
{"id":"login","label":"login_user","source_file":"src/auth.rs","source_location":"L5","community":2,"community_name":"auth"}],
"links":[{"source":"worker","target":"s3","relation":"calls"},{"source":"s3","target":"list","relation":"calls"},
{"source":"login","target":"ext_ui","relation":"references"}]}`

func TestTokens(t *testing.T) {
	if k := fmt.Sprint(Keywords("What functionality scans S3 buckets?")); k != "[scan s3 bucket]" {
		t.Fatalf("keywords %s", k)
	}
}

func TestRouteFindsCommunityAndEntryPoints(t *testing.T) {
	g := New()
	if err := g.LoadFile("api", write(t, t.TempDir(), backend)); err != nil {
		t.Fatal(err)
	}
	comms, hits := g.Route("What functionality scans S3 buckets?", 5)
	if len(comms) == 0 || comms[0] != "api/scanning" {
		t.Fatalf("communities %v", comms)
	}
	if g.Nodes[hits[0].Node].Label != "S3BucketScanner" {
		t.Fatalf("top hit %s", g.Nodes[hits[0].Node].Label)
	}
	sl := g.Expand([]int{hits[0].Node}, 20)
	if len(sl.Nodes) != 3 || len(sl.Edges) != 2 { // scanner + caller worker + callee list
		t.Fatalf("slice %v", sl)
	}
	if c := g.Nodes[sl.Nodes[0]].Cite(); c != "src/scan/s3.rs:L10" {
		t.Fatalf("cite %s", c)
	}
}

func TestCrossRepoMesh(t *testing.T) {
	g := New()
	ui := `{"nodes":[{"id":"ext_ui","label":"ScanButton","source_file":"ui/Scan.tsx","source_location":"L3","community":0}],"links":[]}`
	_ = g.LoadFile("api", write(t, t.TempDir(), backend))
	_ = g.LoadFile("ui", write(t, t.TempDir(), ui))
	// edge from api:login -> ui:ext_ui resolved across repos
	found := false
	for _, e := range g.Edges {
		if g.Nodes[e.From].Repo == "api" && g.Nodes[e.To].Repo == "ui" {
			found = true
		}
	}
	if !found {
		t.Fatal("cross-repo edge not resolved")
	}
	if !g.Known("ui/Scan.tsx", 3) || g.Known("ui/Scan.tsx", 4) || g.Known("nope.go", 0) {
		t.Fatal("Known mismatch")
	}
}

func TestQueryLatency100kNodes(t *testing.T) {
	g := New()
	r := &rawGraph{}
	for i := 0; i < 100000; i++ {
		r.Nodes = append(r.Nodes, Node{ID: fmt.Sprint("n", i), Label: fmt.Sprintf("Func%dHandler", i%5000), File: fmt.Sprintf("pkg%d/f.go", i%300), Loc: "L1", Community: i % 40})
	}
	g.add("big", r)
	start := time.Now()
	_, hits := g.Route("where is func42 handler pkg7", 6)
	var seeds []int
	for _, h := range hits {
		seeds = append(seeds, h.Node)
	}
	g.Expand(seeds, 20)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("query took %v", d)
	}
}

// Real Graphify output: numeric community only, no names, no commit, no community_name.
const realShape = `{"directed":false,"nodes":[
{"id":"a","label":"S3BucketScanner","source_file":"svc/scan/s3.rs","source_location":"L10","community":7},
{"id":"b","label":"list_buckets","source_file":"svc/scan/s3.rs","source_location":"L42","community":7},
{"id":"c","label":"deploy_stack","source_file":"infra/deploy/stack.py","source_location":"L3","community":9},
{"id":"d","label":"helm_release","source_file":"infra/deploy/helm.py","source_location":"L8","community":9}],
"links":[{"source":"a","target":"b","relation":"calls"},{"source":"c","target":"d","relation":"calls"}]}`

func TestRealSchemaCommunityRouting(t *testing.T) {
	g := New()
	if err := g.LoadFile("r", write(t, t.TempDir(), realShape)); err != nil {
		t.Fatal(err)
	}
	comms, _ := g.Route("How is this application deployed? deploy", 5)
	if len(comms) == 0 || comms[0][:2] != "r/" || !strings.Contains(comms[0], "deploy") {
		t.Fatalf("communities %v", comms)
	}
}

func TestCrossRepoCollisionNotLinked(t *testing.T) {
	g := New()
	dangling := `{"nodes":[{"id":"x","label":"Caller","source_file":"a.go","source_location":"L1"}],"links":[{"source":"x","target":"src_main","relation":"calls"}]}`
	other := `{"nodes":[{"id":"src_main","label":"Main","source_file":"m.go","source_location":"L1"}]}`
	g.LoadFile("a", write(t, t.TempDir(), dangling))
	g.LoadFile("b", write(t, t.TempDir(), other))
	if len(g.Edges) != 1 || !g.Edges[0].Inferred {
		t.Fatalf("unique match should link as inferred: %+v", g.Edges)
	}
	g.LoadFile("c", write(t, t.TempDir(), other)) // second candidate: ambiguous, no more edges
	g2 := New()
	g2.LoadFile("b", write(t, t.TempDir(), other))
	g2.LoadFile("c", write(t, t.TempDir(), other))
	g2.LoadFile("a", write(t, t.TempDir(), dangling))
	if len(g2.Edges) != 0 {
		t.Fatalf("ambiguous id must not link: %+v", g2.Edges)
	}
}

func TestShortPrefixDoesNotExpand(t *testing.T) {
	g := New()
	g.LoadFile("r", write(t, t.TempDir(), `{"nodes":[{"id":"1","label":"getter"},{"id":"2","label":"get"}]}`))
	if n := len(g.match("get")); n != 1 {
		t.Fatalf("got %d posting lists", n)
	}
}
