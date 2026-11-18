// Package graph loads Graphify graph.json files (single repo or mesh) and answers
// routing + subgraph queries against an in-memory inverted index.
package graph

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"
)

type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	File      string `json:"source_file"`
	Loc       string `json:"source_location"`
	Community int    `json:"community"`
	CommName  string `json:"community_name"`
	Repo      string `json:"-"`
	Key       string `json:"-"` // repo + ":" + id, unique across the mesh
}

// Line returns the 1-based start line parsed from "L78" / "L78-90", or 0.
func (n *Node) Line() int {
	var l int
	fmt.Sscanf(strings.TrimPrefix(n.Loc, "L"), "%d", &l)
	return l
}

// Cite renders "file:L78" (empty when the node has no location).
func (n *Node) Cite() string {
	if n.File == "" {
		return ""
	}
	if l := n.Line(); l > 0 {
		return fmt.Sprintf("%s:L%d", n.File, l)
	}
	return n.File
}

type Edge struct {
	From, To int // indexes into Graph.Nodes
	Relation string
}

type rawGraph struct {
	Nodes []Node `json:"nodes"`
	Links []struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		Relation string `json:"relation"`
	} `json:"links"`
	Commit string `json:"built_at_commit"`
}

type Graph struct {
	Nodes    []Node
	Edges    []Edge
	Out, In  [][]int // edge indexes per node
	Commits  map[string]string
	byKey    map[string]int
	byID     map[string]int // first node with that raw id, for cross-repo resolution
	postings map[string][]int
	commPost map[string][]int // token -> community slot
	comms    []commInfo
	commOf   []int // node -> comms slot
	locs     map[string]bool
	pending  []pendingEdge // edges whose target is not (yet) in any loaded repo
}

type pendingEdge struct {
	from        int
	target, rel string
}

type commInfo struct {
	Repo, Name string
	ID         int
	Members    []int
}

func New() *Graph {
	return &Graph{Commits: map[string]string{}, byKey: map[string]int{}, byID: map[string]int{},
		postings: map[string][]int{}, commPost: map[string][]int{}}
}

// LoadFile adds one graph.json to the mesh under the given repo name.
func (g *Graph) LoadFile(repo, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var r rawGraph
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	g.add(repo, &r)
	return nil
}

func (g *Graph) add(repo string, r *rawGraph) {
	base := len(g.Nodes)
	g.Commits[repo] = r.Commit
	for i := range r.Nodes {
		n := r.Nodes[i]
		n.Repo, n.Key = repo, repo+":"+n.ID
		if n.Label == "" {
			n.Label = n.ID
		}
		g.byKey[n.Key] = len(g.Nodes)
		if _, ok := g.byID[n.ID]; !ok {
			g.byID[n.ID] = len(g.Nodes)
		}
		g.Nodes = append(g.Nodes, n)
	}
	g.Out = append(g.Out, make([][]int, len(r.Nodes))...)
	g.In = append(g.In, make([][]int, len(r.Nodes))...)
	for _, l := range r.Links {
		f, ok := g.byKey[repo+":"+l.Source]
		if !ok {
			continue
		}
		if t, ok := g.byKey[repo+":"+l.Target]; ok {
			g.link(f, t, l.Relation)
		} else {
			g.pending = append(g.pending, pendingEdge{f, l.Target, l.Relation})
		}
	}
	// resolve cross-repo edges, whichever repo was loaded first
	rest := g.pending[:0]
	for _, p := range g.pending {
		if t, ok := g.byID[p.target]; ok {
			g.link(p.from, t, p.rel)
		} else {
			rest = append(rest, p)
		}
	}
	g.pending = rest
	g.locs = nil
	g.index(base)
}

func (g *Graph) link(f, t int, rel string) {
	g.Edges = append(g.Edges, Edge{f, t, rel})
	e := len(g.Edges) - 1
	g.Out[f] = append(g.Out[f], e)
	g.In[t] = append(g.In[t], e)
}

func (g *Graph) index(base int) {
	slot := map[string]int{}
	for i := range g.comms {
		slot[fmt.Sprintf("%s/%d", g.comms[i].Repo, g.comms[i].ID)] = i
	}
	g.commOf = append(g.commOf, make([]int, len(g.Nodes)-base)...)
	for i := base; i < len(g.Nodes); i++ {
		n := &g.Nodes[i]
		k := fmt.Sprintf("%s/%d", n.Repo, n.Community)
		s, ok := slot[k]
		if !ok {
			s = len(g.comms)
			slot[k] = s
			name := n.CommName
			if name == "" {
				name = fmt.Sprintf("community-%d", n.Community)
			}
			g.comms = append(g.comms, commInfo{Repo: n.Repo, Name: name, ID: n.Community})
		}
		g.comms[s].Members = append(g.comms[s].Members, i)
		g.commOf[i] = s
		for _, t := range uniq(Tokens(n.Label + " " + n.File)) {
			g.postings[t] = append(g.postings[t], i)
		}
	}
	for s := range g.comms {
		for _, t := range uniq(Tokens(g.comms[s].Name)) {
			g.commPost[t] = append(g.commPost[t], s)
		}
	}
}

func uniq(ts []string) []string {
	seen := map[string]bool{}
	out := ts[:0:0]
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Tokens splits identifiers/paths into lowercase words (camelCase, snake_case, paths).
func Tokens(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 1 {
			out = append(out, strings.ToLower(string(cur)))
		}
		cur = cur[:0]
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 &&
			(unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

var stop = map[string]bool{"the": true, "is": true, "are": true, "how": true, "what": true, "does": true, "do": true,
	"this": true, "that": true, "which": true, "where": true, "who": true, "and": true, "for": true, "from": true,
	"with": true, "show": true, "me": true, "of": true, "in": true, "to": true, "it": true, "its": true, "an": true,
	"functionality": true, "application": true, "code": true, "perform": true, "performs": true, "used": true}

// Keywords extracts search concepts from a question, with trivial plural stemming.
func Keywords(q string) []string {
	var out []string
	for _, t := range uniq(Tokens(q)) {
		if stop[t] {
			continue
		}
		if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			t = t[:len(t)-1]
		}
		out = append(out, t)
	}
	return out
}

// match returns posting list for a keyword, also matching longer tokens by prefix
// ("scan" -> "scanner"). Prefix matching scans the vocabulary, which is small.
func (g *Graph) match(kw string) [][]int {
	var res [][]int
	for t, p := range g.postings {
		if t == kw || (len(kw) >= 3 && strings.HasPrefix(t, kw)) {
			res = append(res, p)
		}
	}
	return res
}

type Hit struct {
	Node  int
	Score float64
}

// Route is the two-tier lookup: score communities top-down, then rank symbols
// bottom-up with a boost for membership in the best communities.
func (g *Graph) Route(q string, k int) (comms []string, hits []Hit) {
	kws := Keywords(q)
	cs := map[int]float64{}
	for _, kw := range kws {
		for t, ss := range g.commPost {
			if t == kw || (len(kw) >= 3 && strings.HasPrefix(t, kw)) {
				for _, s := range ss {
					cs[s] += 1
				}
			}
		}
	}
	scores := map[int]float64{}
	for _, kw := range kws {
		for _, p := range g.match(kw) {
			idf := math.Log(1 + float64(len(g.Nodes))/float64(len(p)))
			for _, n := range p {
				scores[n] += idf
			}
		}
	}
	for n := range scores {
		scores[n] *= 1 + 0.5*cs[g.commOf[n]] // community boost
		if len(g.In[n])+len(g.Out[n]) > 0 {
			scores[n] *= 1 + 0.1*math.Log1p(float64(len(g.In[n])+len(g.Out[n])))
		}
	}
	for n, s := range scores {
		hits = append(hits, Hit{n, s})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Node < hits[j].Node
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	// report communities of the top hits, ranked by direct name match then hit count
	cnt := map[int]float64{}
	for _, h := range hits {
		cnt[g.commOf[h.Node]] += h.Score
	}
	for s, v := range cs {
		cnt[s] += v * 100
	}
	var ss []int
	for s := range cnt {
		ss = append(ss, s)
	}
	sort.Slice(ss, func(i, j int) bool { return cnt[ss[i]] > cnt[ss[j]] || (cnt[ss[i]] == cnt[ss[j]] && ss[i] < ss[j]) })
	for i, s := range ss {
		if i == 3 {
			break
		}
		comms = append(comms, g.comms[s].Repo+"/"+g.comms[s].Name)
	}
	return
}

// Slice is a bounded subgraph around seed nodes.
type Slice struct {
	Nodes []int
	Edges []int
}

// Expand grows seeds by BFS over callers/callees (both directions) until max nodes.
// Closest-to-seed nodes win; ties prefer lower index for determinism.
func (g *Graph) Expand(seeds []int, max int) Slice {
	in := map[int]bool{}
	var order []int
	for _, s := range seeds {
		if !in[s] && len(order) < max {
			in[s] = true
			order = append(order, s)
		}
	}
	for head := 0; head < len(order) && len(order) < max; head++ {
		n := order[head]
		var nb []int
		for _, e := range g.Out[n] {
			nb = append(nb, g.Edges[e].To)
		}
		for _, e := range g.In[n] {
			nb = append(nb, g.Edges[e].From)
		}
		sort.Ints(nb)
		for _, m := range nb {
			if !in[m] && len(order) < max {
				in[m] = true
				order = append(order, m)
			}
		}
	}
	sl := Slice{Nodes: order}
	for _, n := range order {
		for _, e := range g.Out[n] {
			if in[g.Edges[e].To] {
				sl.Edges = append(sl.Edges, e)
			}
		}
	}
	return sl
}

// Neighbors returns nodes adjacent to the given set but not in it (for follow-ups).
func (g *Graph) Neighbors(set []int) []int {
	in := map[int]bool{}
	for _, n := range set {
		in[n] = true
	}
	seen := map[int]bool{}
	var out []int
	for _, n := range set {
		for _, e := range g.Out[n] {
			if m := g.Edges[e].To; !in[m] && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
		for _, e := range g.In[n] {
			if m := g.Edges[e].From; !in[m] && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Ints(out)
	return out
}

// Known reports whether file:line is a real location in the graph (line 0 = any line in file).
func (g *Graph) Known(file string, line int) bool {
	if g.locs == nil {
		g.locs = map[string]bool{}
		for i := range g.Nodes {
			n := &g.Nodes[i]
			g.locs[n.File] = true
			g.locs[fmt.Sprintf("%s:%d", n.File, n.Line())] = true
		}
	}
	if line == 0 {
		return g.locs[file]
	}
	return g.locs[fmt.Sprintf("%s:%d", file, line)]
}
