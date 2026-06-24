# rags — Local Semantic Index System

> CLI-first, local-first semantic search for your project files. Think `grep` but powered by meaning, not exact words.

`rags` indexes your text files into a vector database (Qdrant) using [SiliconFlow](https://siliconflow.cn)'s `bge-m3` embedding model, then lets you search by meaning — "后悔值兑换寿命" matches paragraphs about exchanging regret for lifespan, even when those exact words never appear.

## Architecture

```
            ┌──────────────┐
            │   rags CLI   │
            └──────┬───────┘
                   │
        ┌──────────▼──────────┐
        │   Core Engine        │
        │  - indexer           │
        │  - watcher           │
        │  - query engine      │
        └──────────┬──────────┘
                   │
   ┌───────────────┼────────────────┐
   │               │                │
   ▼               ▼                ▼
Config         Embedding        Vector DB
~/.rags/       (SiliconFlow)    (Qdrant)
               bge-m3
               bge-reranker-v2-m3
```

## Quick Start

### Prerequisites

- **Go 1.23+** — to build from source
- **Docker** — to run Qdrant (or an existing Qdrant instance)
- **SiliconFlow API key** — get one at [siliconflow.cn](https://siliconflow.cn)

### Install

```bash
# Clone and build
git clone git@github.com:chenshao107/rags.git
cd rags
go build -o rags ./cmd/rags/

# Install to PATH
mkdir -p ~/.local/bin && cp rags ~/.local/bin/
echo 'export PATH=$HOME/.local/bin:$PATH' >> ~/.bashrc
```

### Start Qdrant

```bash
docker run -d --name qdrant -p 6333:6333 -v qdrant_data:/qdrant/storage qdrant/qdrant
```

### Index your project

```bash
cd ~/my-project
rags init
```

On first run, `rags` will prompt for your SiliconFlow API key and model preferences. These are saved to `~/.rags/settings.yaml` and reused across all projects.

### Search

```bash
rags grep "后悔值兑换寿命"        # Top 5 results
rags grep "主角的成长" -n 10      # Top 10 results
```

## Commands

| Command | Description |
|---------|-------------|
| `rags init` | Initialize a `.rags` project and perform full index |
| `rags update` | Incrementally sync index with file changes + cross-project GC |
| `rags grep <query>` | Semantic search (`-n` flag controls result count) |
| `rags files` | List all indexed files with hashes |
| `rags config` | Show global and project configuration |
| `rags projects` | List all registered projects and their status |

Run `rags --help` or `rags <command> --help` for detailed documentation.

## How It Works

### Search pipeline

```
query → embed (bge-m3, 1024-dim) → Qdrant search (top 50) → rerank (top 10) → print top N
```

### Indexing pipeline

```
walk files → compute hash → compare with state
  ├─ new file      → chunk → embed → upsert
  ├─ modified file → delete old chunks → re-chunk → re-embed → upsert
  ├─ unchanged     → skip
  └─ file deleted  → delete chunks from Qdrant
```

### Incremental update

File-level invalidation: if a file's SHA256 hash changes, all its chunks are replaced. No diff-level or chunk-level partial updates — simple, stable, and recoverable.

### Cross-project GC

Every `rags update` checks `~/.rags/projects.yaml`. If any registered project's directory has been deleted, its Qdrant collection is automatically removed and the entry is cleaned up.

## Configuration

### Global (`~/.rags/`)

```yaml
# ~/.rags/settings.yaml
qdrant:
  url: http://localhost:6333

embedding:
  provider: siliconflow
  base_url: https://api.siliconflow.cn/v1
  api_key: sk-xxx
  model: BAAI/bge-m3

rerank:
  provider: siliconflow
  base_url: https://api.siliconflow.cn/v1
  api_key: sk-xxx
  model: BAAI/bge-reranker-v2-m3
```

`~/.rags/projects.yaml` tracks all initialized projects for cross-project GC.

### Project-local (`.rags/`)

```yaml
# .rags/config.yaml
name: my_project
paths:
  - ./
exclude:
  - .git
  - node_modules
  - vendor
index:
  chunk_size: 800
  overlap: 150
```

```json
// .rags/state.json — file hash manifest
{
  "doc/chapter1.txt": "abc123...",
  "notes/ideas.md": "def456..."
}
```

## Data Model

Each text chunk is stored in Qdrant as:

```json
{
  "id": "<uuid>",
  "vector": [...],
  "payload": {
    "project": "my_project",
    "file_path": "novel/第15章.txt",
    "file_hash": "abc123...",
    "start_line": 120,
    "end_line": 145,
    "chunk_index": 3,
    "text": "后悔值结算：顾景修+4..."
  }
}
```

## Project Structure

```
rags/
├── cmd/rags/main.go          # CLI entrypoint (cobra)
├── internal/
│   ├── config/               # Config management (global + project)
│   ├── embedder/             # SiliconFlow embedding client
│   ├── reranker/             # SiliconFlow rerank client
│   ├── vectordb/             # Qdrant REST API wrapper
│   ├── indexer/              # File scanning, chunking, diff detection
│   ├── query/                # Search pipeline orchestration
│   └── project/              # Project registry + cross-project GC
├── go.mod
└── go.sum
```

## License

MIT
