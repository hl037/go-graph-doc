# go-graph-doc

Call graph visualizer for Go projects, with extracted documentation.  
Interactive interface: resizable tree view, D3 graph, and doc panel.

## Install

```bash
go install go-graph-doc@latest
```

Or build from source:

```bash
git clone ...
cd go-graph-doc
go build -o go-graph-doc .
```

## Usage

```bash
go-graph-doc [flags] [packages...]
```

Without `-o`, starts an HTTP server (default `:8080`).  
With `-o`, writes to a file instead.

### Examples

```bash
# Interactive server on the current module (default pattern is ./...)
go-graph-doc

# Analyze a project in another directory
go-graph-doc -dir /path/to/myproject
go-graph-doc -dir ~/go/src/github.com/myorg/myapp ./cmd/...

# Specific package patterns
go-graph-doc ./internal/...
go-graph-doc ./cmd/myapp

# Focus on one package subtree
go-graph-doc -focus github.com/myorg/myapp/internal ./...

# Export a standalone HTML file
go-graph-doc -html -o callgraph.html ./...

# Export raw JSON
go-graph-doc -json -o callgraph.json ./...

# More precise call graph (requires a main package)
go-graph-doc -algo rta ./cmd/myapp

# Include stdlib and test functions
go-graph-doc -nostd=false -tests ./...

# Custom port
go-graph-doc -addr :9000 ./...
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-dir` | _(current dir)_ | Project directory to analyze |
| `-algo` | `cha` | Call graph algorithm: `cha`, `rta`, `vta` |
| `-focus` | _(all)_ | Only include packages whose path contains this string |
| `-nostd` | `true` | Exclude standard library packages |
| `-novendor` | `true` | Exclude vendor packages |
| `-tests` | `false` | Include test functions |
| `-o` | _(stdout)_ | Output file path (`-` for stdout) |
| `-json` | `false` | Output raw JSON data |
| `-html` | `false` | Output standalone HTML (no server needed) |
| `-addr` | `:8080` | HTTP server listen address |

## Algorithms

| Algorithm | Speed | Precision | Requires |
|-----------|-------|-----------|----------|
| `cha` | Fast | Conservative (may over-approximate) | Any package |
| `rta` | Medium | More precise | A `main` package |
| `vta` | Slower | Most precise | Any package |

Start with `cha` for exploration. Switch to `rta` or `vta` for accuracy on smaller scopes.

## Interface

**Left panel — Tree view**
- Packages and their functions, sorted by source line
- Search bar filters both package paths and function names
- Click a function to select it and center the graph on it

**Center — Call graph**
- **Force**: physics simulation, packages clustered together
- **Radial**: BFS rings from the selected node outward
- **Layered**: topological sort, callers above callees
- Scroll to zoom, drag to pan, drag nodes to reposition
- Normal edges in grey, goroutine edges in orange dashed
- Clicking a node selects it and updates the doc panel

**Right panel — Documentation**
- Full function signature
- Go doc comment
- Source file and line number
- Callers and callees (clickable, in order of appearance in source)
- `← Prev` / `Next →` navigate through click history

## Output formats

**Server mode** (default): live server with hot JSON endpoint at `/data.json`.

**`-html`**: single self-contained HTML file, no server required. Embed in wikis, share with teammates, open offline.

**`-json`**: raw data for use in other tools or scripts. Schema:

```jsonc
{
  "packages": [
    {
      "id": "github.com/foo/bar",
      "name": "bar",
      "functions": [
        {
          "id": "github.com/foo/bar.MyFunc",
          "name": "MyFunc",
          "pkgPath": "github.com/foo/bar",
          "signature": "func MyFunc(x int) error",
          "doc": "MyFunc does something useful.",
          "file": "/path/to/file.go",
          "line": 42,
          "callerIds": ["github.com/foo/bar.Caller"],
          "calleeIds": ["github.com/foo/bar.Callee"]
        }
      ]
    }
  ],
  "edges": [
    { "from": "github.com/foo/bar.Caller", "to": "github.com/foo/bar.MyFunc", "isGoroutine": false }
  ],
  "focusPkg": "",
  "algorithm": "cha"
}
```
