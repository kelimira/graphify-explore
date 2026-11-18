package graph

import (
	"fmt"
	"os"
	"path/filepath"
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
