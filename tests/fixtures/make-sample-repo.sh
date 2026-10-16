#!/bin/sh
# Builds the Plan-Forge sample repository used by the AST, documentation drift
# and ingestion tests (checkpoints 2 and 5).
#
# Usage: make-sample-repo.sh <destination-directory>
#
# The repository holds one Go, TypeScript, TSX, JavaScript, Python and Rust
# file, an HTTP route registration in Go and in JavaScript, and a README that
# documents Foo(a) while the code defines Foo(a, b). It also holds files the
# scanner must not read: an ignored file, a vendored file, a node_modules file
# and a tracked symbolic link, plus one untracked file it must read.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <destination-directory>" >&2
  exit 2
fi

dest=$1
mkdir -p "$dest"
cd "$dest" || exit 1
if [ -e .git ]; then
  echo "$dest already holds a repository" >&2
  exit 1
fi

git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name "Plan-Forge Fixture"
git config user.email "fixture@example.invalid"
git config commit.gpgsign false
git config core.autocrlf false

mkdir -p web tools src vendor/lib node_modules/pkg dist

cat > README.md <<'EOF'
# Sample

Compute sums with `Foo(a)`; the implementation lives in `main.go`.

The health endpoint is `GET /health`.
EOF

cat > main.go <<'EOF'
package main

import (
        "fmt"
        "net/http"
)

// Foo adds its operands.
func Foo(a, b int) int {
        return a + b
}

// Server serves the sample API.
type Server struct {
        Port int
}

// Start registers the routes and begins serving.
func (s *Server) Start(port int) error {
        http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
                fmt.Fprintln(w, "ok")
        })
        return nil
}

func main() {}
EOF

cat > web/client.ts <<'EOF'
export interface Options {
  retries: number;
}

export function connect(url: string, options?: Options): Promise<void> {
  return Promise.resolve();
}
EOF

cat > web/App.tsx <<'EOF'
export function App(props: { name: string }): JSX.Element {
  return <div>{props.name}</div>;
}
EOF

cat > web/util.js <<'EOF'
import express from "express";

export function add(a, b) {
  return a + b;
}

const app = express();
app.get("/users", (req, res) => res.json([]));
EOF

cat > tools/build.py <<'EOF'
import os


def build(name, count=1):
    return os.path.join(name, str(count))


class Builder:
    def run(self, target):
        return build(target)
EOF

cat > src/lib.rs <<'EOF'
use std::fmt;

pub struct Config {
    pub name: String,
}

pub fn parse(input: &str) -> Result<i32, String> {
    input.parse::<i32>().map_err(|e| e.to_string())
}

impl Config {
    pub fn new(name: String) -> Self {
        Config { name }
    }
}
EOF

cat > .gitignore <<'EOF'
dist/
*.log
EOF

cat > vendor/lib/lib.go <<'EOF'
package lib

func Vendored() {}
EOF

cat > node_modules/pkg/index.js <<'EOF'
export function installed() {}
EOF

ln -s main.go link.go

git add -A
git commit -q -m "Add sample sources"

# Files created after the commit: one ignored by .gitignore, one untracked.
cat > dist/ignored.go <<'EOF'
package dist

func Ignored() {}
EOF

cat > tools/untracked.py <<'EOF'
def fresh():
    return 1
EOF
