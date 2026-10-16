//go:build client && !server

package ast

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// parseStatus is the outcome of one parse attempt.
type parseStatus int

const (
	parseOK parseStatus = iota
	// parseTimeout means the per-file parse budget elapsed.
	parseTimeout
	// parseCancelled means the surrounding scan was cancelled.
	parseCancelled
)

// pooledParser is a Tree-sitter parser bound to one grammar, with the
// cancellation flag the runtime polls while parsing. The flag lives in Go
// memory owned by this struct, which keeps it reachable for as long as the C
// parser holds its address.
type pooledParser struct {
	grammar string
	p       *ts.Parser
	flag    *uintptr
}

// parse parses src, abandoning the attempt when the timeout elapses or ctx ends.
// The returned tree must be closed by the caller; it is nil unless the status is
// parseOK.
func (pp *pooledParser) parse(ctx context.Context, timeout time.Duration, src []byte) (*ts.Tree, parseStatus) {
	atomic.StoreUintptr(pp.flag, 0)
	pp.p.Reset()

	pctx, cancel := context.WithTimeout(ctx, timeout)
	done := make(chan struct{})
	stop := context.AfterFunc(pctx, func() {
		atomic.StoreUintptr(pp.flag, 1)
		close(done)
	})
	tree := pp.p.Parse(src, nil)
	if !stop() {
		// The callback is running or has run; wait so it cannot set the flag
		// during the next parse.
		<-done
	}
	cancel()
	atomic.StoreUintptr(pp.flag, 0)

	if tree != nil {
		return tree, parseOK
	}
	pp.p.Reset()
	if ctx.Err() != nil {
		return nil, parseCancelled
	}
	return nil, parseTimeout
}

// parserPool hands out parsers by grammar so concurrent workers never share one.
// It owns every parser it creates and closes them all in Close.
type parserPool struct {
	mu     sync.Mutex
	idle   map[string][]*pooledParser
	all    []*pooledParser
	closed bool
}

func newParserPool() *parserPool {
	return &parserPool{idle: map[string][]*pooledParser{}}
}

// acquire returns an idle parser for the grammar or creates one.
func (pl *parserPool) acquire(cl *compiledLang) (*pooledParser, error) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.closed {
		return nil, fmt.Errorf("ast: parser pool is closed")
	}
	if list := pl.idle[cl.spec.grammar]; len(list) > 0 {
		pp := list[len(list)-1]
		pl.idle[cl.spec.grammar] = list[:len(list)-1]
		return pp, nil
	}
	p := ts.NewParser()
	if err := p.SetLanguage(cl.lang); err != nil {
		p.Close()
		return nil, fmt.Errorf("ast: set %s language: %w", cl.spec.grammar, err)
	}
	pp := &pooledParser{grammar: cl.spec.grammar, p: p, flag: new(uintptr)}
	p.SetCancellationFlag(pp.flag)
	pl.all = append(pl.all, pp)
	return pp, nil
}

// release returns a parser to the pool.
func (pl *parserPool) release(pp *pooledParser) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.idle[pp.grammar] = append(pl.idle[pp.grammar], pp)
}

// Close closes every parser the pool created. Parsers still checked out must
// be released before Close is called.
func (pl *parserPool) Close() {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.closed = true
	for _, pp := range pl.all {
		pp.p.Close()
	}
	pl.all = nil
	pl.idle = map[string][]*pooledParser{}
}

// countErrorNodes counts the ERROR and MISSING nodes in a tree, descending only
// into subtrees that contain an error.
func countErrorNodes(root *ts.Node) int {
	cur := root.Walk()
	defer cur.Close()
	count := 0
	for range 1 << 20 {
		node := cur.Node()
		if node.IsError() || node.IsMissing() {
			count++
		}
		if node.HasError() && cur.GotoFirstChild() {
			continue
		}
		for !cur.GotoNextSibling() {
			if !cur.GotoParent() {
				return count
			}
		}
	}
	return count
}
