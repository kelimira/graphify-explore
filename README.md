# graphify-explore

feat: Create conversational codebase Q&A application powered by Graphify AST graphs
  ## Summary

  Build a standalone conversational application that lets developers ask natural language questions about any codebase (e.g., "How is this application deployed?", "What functionality performs an S3
  scan?").
  The application uses Graphify AST knowledge graphs as a deterministic index. Instead of reading whole repositories or running brute-force grep, it extracts precise subgraphs (entry points, call
  hierarchies, and architectural communities) and provides grounded answers with exact file and line citations at a fraction of standard LLM token costs.
  ──────
  ## Problem Statement
  Current AI code assistants struggle with architectural questions across medium and large repositories:
  1. Context Window Limits & Token Burn: Grepping or reading files blindly into context windows burns tens of thousands of tokens per question.
  2. Hallucinated Call Chains: LLMs guess function names and invent non-existent relationships when code spans multiple packages.
  3. Multi-Repo Blindness: Real applications span companion repositories (e.g., UI, backend API, worker daemon, infrastructure); traditional search cannot resolve cross-repo dependencies.
  ──────
  ## Key Capabilities
  ### 1. Graphify AST Ingestion & Indexing

  • Read Graphify's persistent knowledge graph artifacts (graph.json, communities, God nodes).
  • Support automatic background incremental updates (graphify update) on git changes.
  • Support both single-repository inspection and cross-repository meshes.
  ### 2. Graph-Guided Query Routing (Two-Tier GraphRAG)
  • Top-Down Community Filter: Match the user question against functional clusters (e.g., storage, auth, deployment_cicd, billing) to prune 95% of irrelevant code.
  • Bottom-Up Traversal: Locate matched symbols and traverse caller/callee links (calls, defines, imports, references) to extract a targeted 10–20 node subgraph slice.
  ### 3. Conversational Interface (CLI & Web UI)
  • Provide a clean chat interface with streaming responses.
  • Ground every answer with verifiable file paths and line ranges (src/jobs/worker.rs:L78).
  • Maintain conversation memory to support natural follow-up questions (e.g., "Show me the error handling in that scanner").
  ### 4. Interactive Diagram & Visual Subgraph Export

  • Render Mermaid.js sequence and flow diagrams dynamically from matched call paths.
  • Provide links to open matched symbols directly in the local editor or GitHub.
  ──────
  ## Proposed Architecture & Data Flow

    User Query ("What functionality scans S3 buckets?")
           │
           ▼
    [1. Query Analyzer] ──> Extract concepts & keywords: ["s3", "bucket", "scan"]
           │
           ▼
    [2. Graph Navigator] ──> Query Graphify graph.json:
                              • Find nodes: S3BucketScanner, ListObjectsV2
                              • Trace callers: JobWorker::process_tenant_scan_job
                              • Subgraph slice: 12 nodes, 18 links (~350 tokens)
           │
           ▼
    [3. LLM Synthesizer] ──> Generate concise ASD-STE100 explanation with exact line citations
           │
           ▼
    [Chat Output] ──> Answer + Flow Diagram + Clickable File Links
  ──────
  ## Acceptance Criteria

  [ ] Graph Ingestion: Can ingest and query Graphify graphs containing 100,000+ nodes in under 200ms.
  [ ] Accurate Routing: Correctly identifies the relevant architectural community and entry points for high-level questions (e.g., deployment, authentication, storage pipelines).
  [ ] Provenance Citations: Every structural claim in an answer links to a real file path and line number verified by the AST graph.
  [ ] Token Efficiency: Average context payload sent to the LLM per query is under 1,500 tokens (vs. 20k–50k for raw file ingestion).
  [ ] Multi-Turn Context: Supports follow-up questions by traversing adjacent nodes in the existing subgraph without re-indexing.
  [ ] Cross-Repo Support: Can trace references across multiple indexed repositories in the central mesh.
  ──────
  ## Recommended Tech Stack

  • Backend / Core Engine: Go or Rust for fast graph traversal and low-latency JSON parsing.
  • AST Indexer: Graphify CLI (graphify explain, graphify path, graphify-out/graph.json).
  • LLM Interface: Zero-API / local CLI runner (Claude Code, Gemini CLI, or OpenAI SDK).
  • Interface: Lightweight Web UI (Vite / React or Go HTML template) and CLI interactive terminal mode.
