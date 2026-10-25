//go:build client && !server

package delivery

import (
	"bufio"
	"io"
	"net"
)

// listenLoopback opens an ephemeral loopback listener. It exists so freePort
// has one place to change if the preview ever needs a non-loopback bind.
func listenLoopback() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// splitHostPort is net.SplitHostPort, named here so box.go reads without an
// extra import whose only use is one call.
func splitHostPort(addr string) (string, string, error) {
	return net.SplitHostPort(addr)
}

// scanLines reports each line of r through onLine until r ends. A line longer
// than the buffer is dropped rather than growing it without bound: build output
// is for a human to read, and no human reads a 4 MB line.
func scanLines(r io.Reader, onLine func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
}
