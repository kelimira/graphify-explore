// Package chat turns a question into a grounded, token-bounded graph context,
// keeps follow-up memory, verifies citations and renders Mermaid diagrams.
package chat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"

	"graphify-explore/internal/graph"
)

const (
	MaxNodes     = 20
	TokenBudget  = 1500
	seedCount    = 6
	followUpHits = 1 // below this many keyword hits a question is treated as a follow-up
)

// Session keeps the last slice so follow-ups traverse adjacent nodes without re-indexing.
type Session struct {
	G    *graph.Graph
	Last []int
}

type Context struct {
	Text        string
	Tokens      int
	Communities []string
	Slice       graph.Slice
	Mermaid     string
	Links       []string
}

func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// Ask builds the context for a question and updates session memory.
func (s *Session) Ask(q string) Context {
	g := s.G
	comms, hits := g.Route(q, seedCount)
	var seeds []int
	for _, h := range hits {
		seeds = append(seeds, h.Node)
	}
	if len(hits) < followUpHits && len(s.Last) > 0 {
		// follow-up: pivot to neighbours of the previous slice, keep previous nodes first
		seeds = append(append([]int{}, s.Last[:min(len(s.Last), seedCount)]...), g.Neighbors(s.Last)...)
	}
	sl := g.Expand(seeds, MaxNodes)
	c := Context{Communities: comms, Slice: sl}
	// shrink the slice until the rendered text fits the token budget
	for {
		c.Text = render(g, q, comms, sl)
		c.Tokens = EstimateTokens(c.Text)
		if c.Tokens <= TokenBudget || len(sl.Nodes) <= 1 {
			break
		}
		sl = g.Expand(sl.Nodes[:len(sl.Nodes)-1], len(sl.Nodes)-1)
		c.Slice = sl
	}
	c.Mermaid = Mermaid(g, sl)
	for _, n := range sl.Nodes {
		if l := Link(&g.Nodes[n], "", ""); l != "" {
			c.Links = append(c.Links, l)
		}
	}
	s.Last = sl.Nodes
	return c
}

func render(g *graph.Graph, q string, comms []string, sl graph.Slice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\nCommunities: %s\nNodes:\n", q, strings.Join(comms, ", "))
	for _, n := range sl.Nodes {
		nd := &g.Nodes[n]
		fmt.Fprintf(&b, "- %s [%s] %s\n", nd.Label, nd.Repo, nd.Cite())
	}
	b.WriteString("Edges:\n")
	for _, e := range sl.Edges {
		ed := g.Edges[e]
		fmt.Fprintf(&b, "- %s -%s-> %s\n", g.Nodes[ed.From].Label, ed.Relation, g.Nodes[ed.To].Label)
	}
	return b.String()
}

// Prompt wraps the context with grounding rules for the LLM.
func Prompt(c Context) string {
	return "Answer using ONLY the graph facts below. Use short ASD-STE100 sentences. " +
		"Cite every claim as file:Lnn copied exactly from the Nodes list. Do not invent symbols.\n\n" + c.Text
}

var mermaidID = regexp.MustCompile(`[^A-Za-z0-9_]`)

// Mermaid renders the slice as a flowchart; node ids are index-based to stay syntax-safe.
func Mermaid(g *graph.Graph, sl graph.Slice) string {
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	for _, n := range sl.Nodes {
		fmt.Fprintf(&b, "  n%d[\"%s\"]\n", n, mermaidID.ReplaceAllString(g.Nodes[n].Label, " "))
	}
	for _, e := range sl.Edges {
		ed := g.Edges[e]
		fmt.Fprintf(&b, "  n%d -->|%s| n%d\n", ed.From, mermaidID.ReplaceAllString(ed.Relation, "_"), ed.To)
	}
	return b.String()
}

// Link gives an editor deep link (root = local checkout) or a GitHub blob URL (ghBase = https://github.com/o/r/blob/main).
func Link(n *graph.Node, root, ghBase string) string {
	if n.File == "" {
		return ""
	}
	if ghBase != "" {
		return fmt.Sprintf("%s/%s#L%d", strings.TrimRight(ghBase, "/"), n.File, n.Line())
	}
	return fmt.Sprintf("vscode://file/%s/%s:%d", strings.TrimRight(root, "/"), n.File, n.Line())
}

var citeRe = regexp.MustCompile(`([\w./-]+\.\w+):L(\d+)`)

// Verify marks each file:Lnn citation in an answer as real (in the graph) or not.
func Verify(g *graph.Graph, answer string) (ok, bad []string) {
	for _, m := range citeRe.FindAllStringSubmatch(answer, -1) {
		var l int
		fmt.Sscanf(m[2], "%d", &l)
		if g.Known(m[1], l) {
			ok = append(ok, m[0])
		} else {
			bad = append(bad, m[0])
		}
	}
	return
}

// Runner streams an LLM answer. Zero-API: shells out to a local CLI (e.g. `claude -p`).
type Runner interface {
	Run(ctx context.Context, prompt string, out io.Writer) error
}

type CmdRunner struct{ Argv []string }

func (r CmdRunner) Run(ctx context.Context, prompt string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, r.Argv[0], r.Argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = out
	return cmd.Run()
}

// ContextOnly is the offline fallback: it echoes the verified context instead of calling an LLM.
type ContextOnly struct{}

func (ContextOnly) Run(_ context.Context, prompt string, out io.Writer) error {
	_, err := io.WriteString(out, prompt)
	return err
}

// REPL runs the interactive terminal loop.
func REPL(ctx context.Context, s *Session, r Runner, in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for fmt.Fprint(out, "> "); sc.Scan(); fmt.Fprint(out, "> ") {
		q := strings.TrimSpace(sc.Text())
		if q == "" {
			continue
		}
		if q == "exit" || q == "quit" {
			return
		}
		c := s.Ask(q)
		var buf strings.Builder
		if err := r.Run(ctx, Prompt(c), io.MultiWriter(out, &buf)); err != nil {
			fmt.Fprintf(out, "\nerror: %v\n", err)
			continue
		}
		_, bad := Verify(s.G, buf.String())
		if len(bad) > 0 {
			fmt.Fprintf(out, "\n[unverified citations: %s]", strings.Join(bad, ", "))
		}
		fmt.Fprintf(out, "\n\n```mermaid\n%s```\n", c.Mermaid)
	}
}
