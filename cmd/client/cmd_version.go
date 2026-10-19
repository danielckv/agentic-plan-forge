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

// versionDisplay renders the build version with exactly one leading "v".
// The release pipeline injects the tag itself (v0.1.2) while a source build
// leaves the default "dev", so a caller that prefixes "v" produced "vv0.1.2".
func versionDisplay() string {
	switch {
	case version == "":
		return "dev"
	case version[0] == 'v' || version[0] == 'V':
		return version
	case version[0] >= '0' && version[0] <= '9':
		return "v" + version
	default:
		return version
	}
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
	fmt.Printf("plan-forge %s\n", versionDisplay())
	fmt.Printf("commit:           %s\n", commit)
	fmt.Printf("platform:         %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("go:               %s\n", runtime.Version())
	fmt.Printf("protocol version: %d\n", contracts.ProtocolVersion)
	return exitOK
}
