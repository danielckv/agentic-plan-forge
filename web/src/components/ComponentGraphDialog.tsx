import { useEffect, useMemo, useState } from "react";

import { apiFetch } from "../protocol/bridge";
import type { GraphView } from "../protocol/api";
import { closeGraph, focusGraphNode, setGraphDepth, useSync } from "../store/syncStore";
import {
  focusIdForModule,
  graphSummary,
  layoutGraph,
  neighboursOf,
  nodeContext,
  type PlacedGraphNode,
} from "../ui/componentGraph";
import { Dialog } from "../ui/Dialog";

/**
 * The 2D component graph, as a large overlay.
 *
 * It draws what the repository scan already knows: files and folders, with the
 * imports between them. Selecting a node shows what it is; selecting again
 * re-centres the graph on it. The state (open, centre, depth) lives in the
 * store, so closing the overlay and opening another page keeps the view exactly
 * where it was.
 *
 * Deliberately not a second 3D view: plan nodes live in the planning page, this
 * is the codebase itself, flat and readable, with a depth control instead of an
 * orbit control so a reader can choose how far out the picture goes.
 */
export function ComponentGraphDialog(): React.ReactElement {
  const sync = useSync();
  const [view, setView] = useState<GraphView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const { graphOpen, graphDepth, graphFocus } = sync;

  useEffect(() => {
    if (!graphOpen) return;
    let cancelled = false;
    setLoading(true);
    const params = new URLSearchParams();
    if (graphFocus !== "") params.set("focus", graphFocus);
    params.set("depth", String(graphDepth));
    const suffix = params.toString() === "" ? "" : `?${params.toString()}`;
    apiFetch(`/api/component-graph${suffix}`)
      .then(async (res) => {
        if (cancelled) return;
        if (!res.ok) throw new Error(`the graph endpoint answered ${String(res.status)}`);
        const body = (await res.json()) as GraphView;
        if (cancelled) return;
        setView(body);
        setError(null);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "the graph could not be loaded");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [graphOpen, graphFocus, graphDepth]);

  const layout = useMemo(() => (view === null ? null : layoutGraph(view)), [view]);
  const selectedNode = useMemo(() => {
    if (view === null || selected === null) return null;
    return view.nodes.find((n) => n.id === selected) ?? null;
  }, [view, selected]);

  return (
    <Dialog
      open={graphOpen}
      onClose={closeGraph}
      size="full"
      title="Component graph"
      description={view === null ? "Reading the repository snapshot." : graphSummary(view)}
    >
      <div className="pf-graph">
        <div className="pf-graph-controls">
          <label className="pf-graph-depth">
            <span>Depth</span>
            <input
              type="range"
              min={1}
              max={4}
              step={1}
              value={graphDepth}
              onChange={(e) => setGraphDepth(Number(e.target.value))}
              aria-label="How many hops from the centre to draw"
            />
            <span className="pf-mono">{graphDepth}</span>
          </label>
          <button
            type="button"
            className="pf-btn pf-btn--ghost"
            onClick={() => focusGraphNode("")}
            disabled={graphFocus === ""}
          >
            Whole repository
          </button>
          <ReopenLink />
          {loading && <span className="pf-caption">loading…</span>}
        </div>

        {error !== null ? (
          <p className="pf-graph-empty" role="alert">
            {error}. The repository has to be scanned before its graph can be drawn.
          </p>
        ) : layout === null || layout.nodes.length === 0 ? (
          <p className="pf-graph-empty">
            {view !== null && !view.ready
              ? "No repository snapshot yet. Scan the codebase first."
              : "Nothing to draw at this focus and depth."}
          </p>
        ) : (
          <div className="pf-graph-body">
            <svg
              className="pf-graph-canvas"
              viewBox={`0 0 ${String(layout.width)} ${String(layout.height)}`}
              role="img"
              aria-label="Files and folders of the repository, with the imports between them"
            >
              {layout.edges.map((edge) => (
                <line
                  key={`${edge.from.node.id}->${edge.to.node.id}`}
                  x1={edge.from.x}
                  y1={edge.from.y}
                  x2={edge.to.x}
                  y2={edge.to.y}
                  className={`pf-graph-edge pf-graph-edge--${edge.kind}`}
                />
              ))}
              {layout.nodes.map((p) => (
                <GraphNodeDot
                  key={p.node.id}
                  placed={p}
                  selected={p.node.id === selected}
                  onSelect={() => setSelected(p.node.id)}
                  onRecentre={() => focusGraphNode(p.node.id)}
                />
              ))}
            </svg>
            <aside className="pf-graph-detail" aria-live="polite">
              {selectedNode === null || view === null ? (
                <p className="pf-body">
                  Select a node to see what it is. Select it again to centre the graph on it.
                </p>
              ) : (
                <NodeDetail node={selectedNode} view={view} />
              )}
            </aside>
          </div>
        )}
      </div>
    </Dialog>
  );
}

/**
 * Copies a link that reopens this session in a browser tab.
 *
 * A launch URL carries a nonce that lives ten seconds and is redeemable once,
 * so a link cannot be stored and reused. It is minted on demand instead, from
 * the authenticated bridge: this button asks for a fresh one and copies it.
 */
function ReopenLink(): React.ReactElement {
  const [state, setReopened] = useState<"idle" | "copying" | "copied" | "failed">("idle");

  const copy = (): void => {
    setReopened("copying");
    void (async () => {
      try {
        const res = await apiFetch("/api/launch-url", { method: "POST", body: "{}" });
        if (!res.ok) throw new Error(String(res.status));
        const body = (await res.json()) as { url?: unknown };
        if (typeof body.url !== "string" || body.url === "") throw new Error("no url");
        await navigator.clipboard.writeText(body.url);
        setReopened("copied");
      } catch {
        setReopened("failed");
      }
    })();
  };

  return (
    <button
      type="button"
      className="pf-btn pf-btn--ghost"
      onClick={copy}
      disabled={state === "copying"}
      title="Copy a link that reopens this session in a browser tab"
    >
      {state === "copied"
        ? "Link copied"
        : state === "failed"
          ? "Could not copy"
          : "Copy reopen link"}
    </button>
  );
}

function GraphNodeDot({
  placed,
  selected,
  onSelect,
  onRecentre,
}: {
  placed: PlacedGraphNode;
  selected: boolean;
  onSelect: () => void;
  onRecentre: () => void;
}): React.ReactElement {
  const { node, x, y } = placed;
  const isModule = node.kind === "module";
  return (
    <g
      className={`pf-graph-node pf-graph-node--${node.kind}${selected ? " is-selected" : ""}`}
      transform={`translate(${String(x)} ${String(y)})`}
      onClick={() => {
        if (selected) onRecentre();
        else onSelect();
      }}
      role="button"
      tabIndex={0}
      aria-label={`${node.label}, ${String(node.symbols)} declarations`}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          if (selected) onRecentre();
          else onSelect();
        }
      }}
    >
      {isModule ? (
        <rect x={-9} y={-9} width={18} height={18} rx={3} />
      ) : (
        <circle r={node.depth === 0 ? 10 : 7} />
      )}
      <text x={0} y={isModule ? 26 : 22} textAnchor="middle">
        {node.label}
      </text>
    </g>
  );
}

function NodeDetail({ node, view }: { node: GraphView["nodes"][number]; view: GraphView }): React.ReactElement {
  const n = neighboursOf(node, view);
  const ctx = nodeContext(node, { imports: [...n.imports, ...n.incoming], contained: [] });
  return (
    <div className="pf-graph-context">
      <h3 className="pf-graph-context-title">{ctx.title}</h3>
      <p className="pf-caption">{ctx.subtitle}</p>
      <dl className="pf-graph-rows">
        {ctx.rows.map((row) => (
          <div key={row.label} className="pf-graph-row">
            <dt>{row.label}</dt>
            <dd className={row.label === "Path" ? "pf-mono" : undefined}>{row.value}</dd>
          </div>
        ))}
      </dl>
      {node.kind === "module" && (
        <button type="button" className="pf-btn pf-btn--ghost" onClick={() => focusGraphNode(focusIdForModule(node.path))}>
          Centre on this folder
        </button>
      )}
      <p className="pf-caption">
        Every line is an import the scanner recorded. A node with no line has no import in this snapshot.
      </p>
    </div>
  );
}
