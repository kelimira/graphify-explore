# gexplore

Ask natural-language questions about a codebase. `gexplore` loads Graphify `graph.json`
files, picks a small relevant subgraph, and hands only that to a local LLM CLI, so
answers cite real `file:Lnn` locations from the AST graph.

## Usage

```
gexplore -graph api=api/graphify-out/graph.json [-graph ui=ui/graphify-out/graph.json] \
         [-llm 'claude -p'] [-web 127.0.0.1:8080] [-update] [-root /path/to/checkout] [-github https://github.com/o/r/blob/main]
```

- No `-llm`: prints the grounded prompt/context only (offline).
- `-graph [name=]path`: repeat for a multi-repo mesh. Without a name, the repo name is the directory above `graphify-out`.
- `-web`: chat UI with streaming, citation check, links and a Mermaid diagram. A bare `:port` binds loopback only; requests need a matching `Host`, `POST` and an `X-Gexplore` header, bodies are capped at 4 KiB, and questions run one at a time.
- `-update`: at startup, runs `graphify update` if the graph is older than git HEAD (or its `built_at_commit` differs). This is a one-shot check, not a watcher.

## How it works

1. Keywords are extracted from the question (camelCase/snake_case aware, light stemming).
2. Communities are scored top-down; symbols are scored bottom-up with an inverted index, boosted by community match and connectivity.
3. The top seeds are expanded over calls/imports/defines edges (both directions) to at most 20 nodes, shrunk until the rendered context fits about 1,500 tokens. Tokens are estimated as `len/4`, not counted with a real tokenizer.
4. Follow-ups ("that scanner", "it") bias scoring toward the previous slice and its neighbours; no re-indexing.
5. After the answer, citations (`file:L78` or `file:L78-90`, checked by start line) are verified against the graph; unknown ones are flagged.

## Graph schema assumptions

Reads `nodes[]` (`id`, `label`, `source_file`, `source_location` like `L78`, `community`) and `links[]` (`source`, `target`, `relation`).
Graphify emits only a numeric `community`, so communities are named from their most distinctive member label/path tokens. An optional `community_name` and `built_at_commit` are honoured when present.

Cross-repo edges: a link whose target is not in its own repo is resolved only when exactly one node in another repo has that id, and is marked `(inferred cross-repo)`. Ambiguous ids are left unlinked.

## Development

`go vet ./... && go test ./...`

## Notes

- The web UI keeps one global session (single-user, loopback only); browser tabs share follow-up memory.
- Mermaid is loaded from jsDelivr (pinned, SRI-checked), so the diagram needs network; the chat itself does not.
- `-update` is a one-shot check at startup (graph mtime vs. git HEAD), not a background watcher.
- Latency: query time on a synthetic 100k-node graph is covered by a test; JSON load time grows with file size and is about 0.2 s for 100k nodes on a dev laptop (`go test -run Load100k -v ./internal/graph`); it is paid once at startup, so the 200 ms criterion applies to queries.
