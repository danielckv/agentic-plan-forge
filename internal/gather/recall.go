//go:build client && !server

package gather

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// queryRecall searches one recall index. worktree selects the rows of this
// working tree in the current schema; it is ignored by the legacy schema,
// which has no such column. ok is false when the file is missing or holds no
// rows for this working tree, so the caller moves on to the next source.
func queryRecall(ctx context.Context, path, worktree string, terms []string, limit int) (hits []contracts.ContextHit, ok bool, err error) {
	if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
		return nil, false, nil
	}
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return nil, false, err
	}
	defer db.Close()

	columns, err := chunkColumns(ctx, db)
	if err != nil {
		return nil, false, err
	}
	if !columns["path"] || !columns["text"] {
		return nil, false, errors.New("the chunks table has no path and text columns")
	}
	scoped := columns["worktree"]

	var rows int
	countSQL := "SELECT count(*) FROM chunks"
	args := []any{}
	if scoped {
		countSQL += " WHERE worktree = ?"
		args = append(args, worktree)
	}
	if err := db.QueryRowContext(ctx, countSQL, args...).Scan(&rows); err != nil {
		return nil, false, err
	}
	if rows == 0 {
		return nil, false, nil
	}

	q := matchQuery(terms)
	searchSQL := "SELECT path, text, bm25(chunks) FROM chunks WHERE chunks MATCH ?"
	args = []any{q}
	if scoped {
		searchSQL += " AND worktree = ?"
		args = append(args, worktree)
	}
	searchSQL += " ORDER BY bm25(chunks) LIMIT ?"
	args = append(args, limit*2)
	res, err := db.QueryContext(ctx, searchSQL, args...)
	if err != nil {
		return nil, false, err
	}
	defer res.Close()
	for res.Next() {
		var p, text string
		var rank float64
		if err := res.Scan(&p, &text, &rank); err != nil {
			return nil, false, err
		}
		if !safeRel(p) || strings.TrimSpace(text) == "" {
			continue
		}
		hits = append(hits, contracts.ContextHit{
			Path: p, Heading: headingOf(text), Text: clip(text),
			// bm25 is lower-is-better and negative; flip it so higher is better.
			Score: math.Round(-rank*1000) / 1000,
		})
	}
	if err := res.Err(); err != nil {
		return nil, false, err
	}
	if hits == nil {
		hits = []contracts.ContextHit{}
	}
	return rankHits(hits, limit), true, nil
}

// openReadOnly opens a SQLite file without write access. A database in WAL
// mode whose shared-memory file cannot be created (a read-only directory) is
// retried as immutable, which reads the main file only.
func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	base := "file:" + (&url.URL{Path: path}).EscapedPath()
	var lastErr error
	for _, params := range []string{"?mode=ro&_pragma=busy_timeout(2000)", "?mode=ro&immutable=1"} {
		db, err := sql.Open("sqlite", base+params)
		if err != nil {
			lastErr = err
			continue
		}
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			lastErr = err
			continue
		}
		var probe int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&probe); err != nil {
			db.Close()
			lastErr = err
			continue
		}
		return db, nil
	}
	return nil, fmt.Errorf("open %s read-only: %w", "recall.db", lastErr)
}

// chunkColumns returns the column names of the chunks table.
func chunkColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info('chunks')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errors.New("no chunks table")
	}
	return cols, nil
}

// matchQuery renders terms as an FTS5 OR-query of quoted words. Terms are
// letters and digits only, so quoting is enough to keep them from being read
// as FTS5 syntax.
func matchQuery(terms []string) string {
	if len(terms) > maxQueryTerms {
		terms = terms[:maxQueryTerms]
	}
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, "")+`"`)
	}
	return strings.Join(quoted, " OR ")
}
