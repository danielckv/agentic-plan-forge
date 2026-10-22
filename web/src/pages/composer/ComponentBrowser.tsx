import { useEffect, useState } from "react";
import { Box, Braces, ChevronRight, CircleDot, Search, SquareFunction, Variable, Workflow, X } from "lucide-react";

import { fetchComponents, type ComponentRef, type ComponentsView } from "../../protocol/api";

/**
 * The codebase browser. It lists the repository's blocks (directories), opens
 * one to show its public declarations, and searches names across the whole
 * repository. Choosing a declaration hands its name to `onPick`, which the
 * composer inserts as a term, so the requirements use the names the code uses.
 */

function KindIcon({ kind }: { kind: string }): React.ReactElement {
  switch (kind) {
    case "function":
    case "method":
      return <SquareFunction size={13} />;
    case "struct":
    case "class":
    case "interface":
    case "trait":
    case "type":
    case "enum":
      return <Braces size={13} />;
    case "constant":
    case "variable":
      return <Variable size={13} />;
    case "route":
      return <Workflow size={13} />;
    default:
      return <CircleDot size={13} />;
  }
}

function SymbolRow({ c, onPick }: { c: ComponentRef; onPick: (c: ComponentRef) => void }): React.ReactElement {
  return (
    <li>
      <button
        type="button"
        className="pf-symbol"
        onClick={() => onPick(c)}
        title={c.signature !== undefined && c.signature !== "" ? c.signature : `${c.kind} ${c.name}`}
      >
        <span className="pf-symbol-kind" data-kind={c.kind} aria-hidden="true">
          <KindIcon kind={c.kind} />
        </span>
        <span className="pf-symbol-name">{c.name}</span>
        <span className="pf-symbol-where">
          {c.path.split("/").pop()}:{c.line}
        </span>
      </button>
    </li>
  );
}

function Block(props: { path: string; language: string; count: number; onPick: (c: ComponentRef) => void }): React.ReactElement {
  const { path, language, count, onPick } = props;
  const [open, setOpen] = useState(false);
  const [symbols, setSymbols] = useState<ComponentRef[] | null>(null);
  useEffect(() => {
    if (!open || symbols !== null) return;
    let live = true;
    fetchComponents({ module: path }).then(
      (v) => {
        if (live) setSymbols(v.symbols);
      },
      () => {
        if (live) setSymbols([]);
      },
    );
    return () => {
      live = false;
    };
  }, [open, path, symbols]);
  return (
    <li className="pf-block" data-open={open}>
      <button type="button" className="pf-block-head" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <ChevronRight size={13} className="pf-block-chevron" aria-hidden="true" />
        <Box size={13} aria-hidden="true" />
        <span className="pf-block-path">{path}</span>
        <span className="pf-block-count">
          {language} · {count}
        </span>
      </button>
      {open && (
        <ul className="pf-symbol-list">
          {symbols === null ? (
            <li className="pf-caption pf-block-loading">Loading…</li>
          ) : symbols.length === 0 ? (
            <li className="pf-caption pf-block-loading">No public declarations.</li>
          ) : (
            symbols.map((c) => <SymbolRow key={c.id} c={c} onPick={onPick} />)
          )}
        </ul>
      )}
    </li>
  );
}

export function ComponentBrowser(props: {
  blocks: ComponentsView | null;
  scanning: boolean;
  onPick: (c: ComponentRef) => void;
}): React.ReactElement {
  const { blocks, scanning, onPick } = props;
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ComponentsView | null>(null);

  useEffect(() => {
    const q = query.trim();
    if (q === "") {
      setResults(null);
      return;
    }
    let live = true;
    const id = setTimeout(() => {
      fetchComponents({ q }).then(
        (v) => {
          if (live) setResults(v);
        },
        () => undefined,
      );
    }, 150);
    return () => {
      live = false;
      clearTimeout(id);
    };
  }, [query]);

  if (blocks === null || !blocks.ready) {
    return (
      <div className="pf-rail-empty">
        <p className="pf-body">{scanning ? "Reading the repository…" : "The repository scan found no code to show."}</p>
        <p className="pf-caption">Blocks and components appear here once the scan finishes.</p>
      </div>
    );
  }

  return (
    <div className="pf-browser">
      <p className="pf-caption">
        Use the names your code already uses. Click one to write it into the requirements, or type <kbd className="pf-kbd">@</kbd>{" "}
        in the editor.
      </p>
      <div className="pf-search">
        <Search size={14} className="pf-search-icon" aria-hidden="true" />
        <input
          type="search"
          className="pf-search-input"
          value={query}
          placeholder="Search components…"
          aria-label="Search components by name or path"
          onChange={(e) => setQuery(e.target.value)}
        />
        {query !== "" && (
          <button type="button" className="pf-search-clear" aria-label="Clear search" onClick={() => setQuery("")}>
            <X size={13} />
          </button>
        )}
      </div>
      {blocks.stack.length > 0 && (
        <div className="pf-chip-list">
          {blocks.stack.map((s) => (
            <span key={s} className="pf-chip">
              {s}
            </span>
          ))}
        </div>
      )}
      {results !== null ? (
        results.symbols.length === 0 ? (
          <p className="pf-caption">No component matches “{query.trim()}”.</p>
        ) : (
          <ul className="pf-symbol-list pf-symbol-list--flat">
            {results.symbols.map((c) => (
              <SymbolRow key={c.id} c={c} onPick={onPick} />
            ))}
            {results.truncated && <li className="pf-caption">Showing the first {results.symbols.length}. Refine the search.</li>}
          </ul>
        )
      ) : (
        <ul className="pf-block-list">
          {blocks.modules.map((m) => (
            <Block key={`${m.path}:${m.language}`} path={m.path} language={m.language} count={m.symbols} onPick={onPick} />
          ))}
        </ul>
      )}
    </div>
  );
}
