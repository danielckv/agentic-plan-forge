//go:build client && !server

package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func init() {
	register("version", "print version, commit, platform and protocol version", runVersion)
}

func runVersion(_ context.Context, args []string) int {
	fs, err := parseFlags("version", args, nil)
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(fs.Output(), "version takes no arguments")
		return exitUsage
	}
	fmt.Printf("plan-forge %s\n", version)
	fmt.Printf("commit:           %s\n", commit)
	fmt.Printf("platform:         %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("go:               %s\n", runtime.Version())
	fmt.Printf("protocol version: %d\n", contracts.ProtocolVersion)
	return exitOK
}
