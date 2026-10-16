//go:build client && server

package main

// Compiled only when both tags are set, which is exactly the case this file
// exists to reject: importing internal/buildguard pulls in the undefined
// identifier that makes the build fail with a readable message.
import _ "github.com/danielckv/agentic-plan-forge/internal/buildguard"
