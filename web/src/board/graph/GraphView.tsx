import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Maximize2, Minus, Plus } from "lucide-react";

import { boardStore } from "../boardStore";
import type { BoardPriority, BoardSnapshot } from "../types";
import { DiscoveryPanel } from "./DiscoveryPanel";
import { GraphEdgeView, GraphNodeView } from "./GraphNodes";
import type { HoverState } from "./GraphNodes";
import { graphNeighborhood, graphSignature, layoutGraph } from "./layout";
import type { GraphNode } from "./layout";

export const MIN_ZOOM = 0.25;
export const MAX_ZOOM = 2.5;
const FIT_MARGIN = 32;
const DRAG_THRESHOLD = 4;
const PAN_STEP = 80;

interface View {
  x: number;
  y: number;
  k: number;
}

export function clampZoom(value: number): number {
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, value));
}

interface GraphViewProps {
  snapshot: BoardSnapshot;
  pending: boolean;
  nowMs: number;
  onOpenTask: (taskID: string) => void;
  registerNode: (taskID: string, element: SVGGElement | null) => void;
  /** Open this discovery (the coordinator panel's "Review in graph" jump); `seq` re-triggers it. */
  focusDiscovery: { id: string; seq: number } | null;
}

const MARKER_KINDS: Array<{ id: string; className: string }> = [
  { id: "depends", className: "pf-graph-arrow--depends" },
  { id: "discovered", className: "pf-graph-arrow--discovered" },
  { id: "path", className: "pf-graph-arrow--path" },
  { id: "cycle", className: "pf-graph-arrow--cycle" },
  { id: "hl", className: "pf-graph-arrow--hl" },
];

export function GraphView({
  snapshot,
  pending,
  nowMs,
  onOpenTask,
  registerNode,
  focusDiscovery,
}: GraphViewProps): React.ReactElement {
  const [showDismissed, setShowDismissed] = useState(false);
  const [showPaths, setShowPaths] = useState(true);
  const [hoverKey, setHoverKey] = useState<string | null>(null);
  const [openDiscoveryID, setOpenDiscoveryID] = useState<string | null>(null);
  const [seenFocusSeq, setSeenFocusSeq] = useState<number | null>(null);
  if (focusDiscovery !== null && focusDiscovery.seq !== seenFocusSeq) {
    // Adjusting state while rendering is React's pattern for reacting to a changed prop.
    setSeenFocusSeq(focusDiscovery.seq);
    setOpenDiscoveryID(focusDiscovery.id);
  }

  const viewport = useRef<HTMLDivElement>(null);
  const surface = useRef<SVGGElement>(null);
  const zoomLabel = useRef<HTMLSpanElement>(null);
  const view = useRef<View>({ x: 0, y: 0, k: 1 });
  const fitted = useRef(false);
  const drag = useRef<{ id: number; startX: number; startY: number; originX: number; originY: number; active: boolean } | null>(null);
  const suppressClick = useRef(false);
  const hoverSuspended = useRef(false);

  // Keyed on a structural signature, not on the arrays: every push rebuilds
  // them, but a push that only moves timestamps must not re-run the layout.
  const signature = graphSignature(snapshot.tasks, snapshot.discoveries);
  const layout = useMemo(
    () => layoutGraph(snapshot.tasks, snapshot.discoveries, { showDismissed, showPaths }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `signature` covers every field the layout reads.
    [signature, showDismissed, showPaths],
  );
  const neighborhood = useMemo(
    () => (hoverKey === null ? null : graphNeighborhood(layout, hoverKey)),
    [layout, hoverKey],
  );

  // The view lives in a ref and is written straight to the DOM: panning and
  // zooming never re-render React.
  const applyView = useCallback(() => {
    const current = view.current;
    surface.current?.setAttribute(
      "transform",
      `translate(${current.x.toFixed(2)} ${current.y.toFixed(2)}) scale(${current.k.toFixed(4)})`,
    );
    if (zoomLabel.current !== null) zoomLabel.current.textContent = `${String(Math.round(current.k * 100))}%`;
  }, []);

  const fit = useCallback(() => {
    const element = viewport.current;
    if (element === null || layout.width === 0) return;
    const rect = element.getBoundingClientRect();
    const k = clampZoom(
      Math.min(
        (rect.width - FIT_MARGIN * 2) / layout.width,
        (rect.height - FIT_MARGIN * 2) / layout.height,
        1.15,
      ),
    );
    view.current = {
      k,
      x: (rect.width - layout.width * k) / 2,
      y: (rect.height - layout.height * k) / 2,
    };
    applyView();
  }, [applyView, layout.height, layout.width]);

  useLayoutEffect(() => {
    if (fitted.current || layout.nodes.length === 0) return;
    fitted.current = true;
    fit();
  }, [fit, layout.nodes.length]);

  const zoomAt = useCallback(
    (factor: number, px?: number, py?: number) => {
      const element = viewport.current;
      if (element === null) return;
      const rect = element.getBoundingClientRect();
      const originX = px ?? rect.width / 2;
      const originY = py ?? rect.height / 2;
      const current = view.current;
      const k = clampZoom(current.k * factor);
      const ratio = k / current.k;
      view.current = {
        k,
        x: originX - (originX - current.x) * ratio,
        y: originY - (originY - current.y) * ratio,
      };
      applyView();
    },
    [applyView],
  );

  // React registers wheel listeners as passive, so zoom needs a native one.
  useEffect(() => {
    const element = viewport.current;
    if (element === null) return;
    const onWheel = (event: WheelEvent): void => {
      event.preventDefault();
      const rect = element.getBoundingClientRect();
      const delta = Math.max(-120, Math.min(120, event.deltaY));
      zoomAt(Math.exp(-delta * 0.0016), event.clientX - rect.left, event.clientY - rect.top);
    };
    element.addEventListener("wheel", onWheel, { passive: false });
    return () => element.removeEventListener("wheel", onWheel);
  }, [zoomAt]);

  const reveal = useCallback(
    (node: GraphNode) => {
      const element = viewport.current;
      if (element === null) return;
      const rect = element.getBoundingClientRect();
      const current = view.current;
      const left = current.x + node.x * current.k;
      const top = current.y + node.y * current.k;
      const right = left + node.w * current.k;
      const bottom = top + node.h * current.k;
      const margin = 24;
      if (left >= margin && top >= margin && right <= rect.width - margin && bottom <= rect.height - margin) return;
      view.current = {
        k: current.k,
        x: rect.width / 2 - (node.x + node.w / 2) * current.k,
        y: rect.height / 2 - (node.y + node.h / 2) * current.k,
      };
      applyView();
    },
    [applyView],
  );

  const latestLayout = useRef(layout);
  useEffect(() => {
    latestLayout.current = layout;
  }, [layout]);

  // Reveal only when the open discovery changes; a push must never snap a panned viewport back.
  useEffect(() => {
    if (openDiscoveryID === null) return;
    const node = latestLayout.current.byKey.get(`d:${openDiscoveryID}`);
    if (node !== undefined) reveal(node);
  }, [openDiscoveryID, reveal]);

  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>): void => {
    if (event.button !== 0) return;
    drag.current = {
      id: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      originX: view.current.x,
      originY: view.current.y,
      active: false,
    };
  };
  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>): void => {
    const state = drag.current;
    if (state === null || state.id !== event.pointerId) return;
    const dx = event.clientX - state.startX;
    const dy = event.clientY - state.startY;
    if (!state.active) {
      if (Math.hypot(dx, dy) < DRAG_THRESHOLD) return;
      state.active = true;
      hoverSuspended.current = true;
      viewport.current?.setPointerCapture(event.pointerId);
      viewport.current?.setAttribute("data-dragging", "true");
    }
    view.current = { k: view.current.k, x: state.originX + dx, y: state.originY + dy };
    applyView();
  };
  const endDrag = (event: React.PointerEvent<HTMLDivElement>): void => {
    const state = drag.current;
    if (state === null || state.id !== event.pointerId) return;
    drag.current = null;
    if (state.active) {
      suppressClick.current = true;
      window.setTimeout(() => {
        suppressClick.current = false;
        hoverSuspended.current = false;
      }, 0);
      viewport.current?.removeAttribute("data-dragging");
      if (viewport.current?.hasPointerCapture(event.pointerId) === true) {
        viewport.current.releasePointerCapture(event.pointerId);
      }
    }
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>): void => {
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    const step = event.shiftKey ? PAN_STEP * 3 : PAN_STEP;
    const pan = (dx: number, dy: number): void => {
      event.preventDefault();
      view.current = { k: view.current.k, x: view.current.x + dx, y: view.current.y + dy };
      applyView();
    };
    switch (event.key) {
      case "ArrowLeft":
        pan(step, 0);
        break;
      case "ArrowRight":
        pan(-step, 0);
        break;
      case "ArrowUp":
        pan(0, step);
        break;
      case "ArrowDown":
        pan(0, -step);
        break;
      case "+":
      case "=":
        event.preventDefault();
        zoomAt(1.25);
        break;
      case "-":
      case "_":
        event.preventDefault();
        zoomAt(0.8);
        break;
      case "0":
        event.preventDefault();
        fit();
        break;
      default:
    }
  };

  const hoverNode = useCallback((key: string | null) => {
    if (key !== null && hoverSuspended.current) return;
    setHoverKey(key);
  }, []);

  const openDiscovery =
    openDiscoveryID === null
      ? undefined
      : snapshot.discoveries.find((discovery) => discovery.id === openDiscoveryID);

  // A discovery that leaves the snapshot (dismissed-and-hidden, plan changed)
  // must not leave a stale panel behind.
  const selectedKey = openDiscovery === undefined ? null : `d:${openDiscovery.id}`;

  const restoreFocus = useCallback((id: string) => {
    window.requestAnimationFrame(() => {
      const root = viewport.current;
      if (root === null) return;
      const escaped = CSS.escape(id);
      const target =
        root.querySelector<SVGGElement>(`[data-node-key="d:${escaped}"]`) ??
        root.querySelector<SVGGElement>(`[data-node-key="t:${escaped}"]`);
      (target ?? root).focus();
    });
  }, []);

  const closePanel = useCallback(() => {
    const id = openDiscoveryID;
    setOpenDiscoveryID(null);
    if (id !== null) restoreFocus(id);
  }, [openDiscoveryID, restoreFocus]);

  const activate = useCallback(
    (node: GraphNode) => {
      if (suppressClick.current) return;
      if (node.taskID !== null) {
        setOpenDiscoveryID(null);
        onOpenTask(node.taskID);
      } else if (node.discoveryID !== null) {
        setOpenDiscoveryID(node.discoveryID);
      }
    },
    [onOpenTask],
  );

  const accept = (priority: BoardPriority): void => {
    if (openDiscovery === undefined) return;
    const id = openDiscovery.id;
    setOpenDiscoveryID(null);
    void boardStore.resolveDiscovery(id, "accept", priority);
    restoreFocus(id);
  };
  const dismiss = (): void => {
    if (openDiscovery === undefined) return;
    const id = openDiscovery.id;
    setOpenDiscoveryID(null);
    void boardStore.resolveDiscovery(id, "dismiss");
    restoreFocus(id);
  };

  const dismissedCount = snapshot.discoveries.filter((discovery) => discovery.status === "dismissed").length;
  const pathCount = snapshot.discoveries.filter((discovery) => discovery.kind === "path").length;
  const proposedCount = snapshot.discoveries.filter((discovery) => discovery.status === "proposed").length;

  return (
    <section className="pf-graph" aria-label="Plan dependency graph">
      <div className="pf-graph-toolbar">
        <label className="pf-graph-toggle">
          <input type="checkbox" checked={showPaths} onChange={(event) => setShowPaths(event.target.checked)} />
          <span>Show paths <em>{pathCount}</em></span>
        </label>
        <label className="pf-graph-toggle">
          <input type="checkbox" checked={showDismissed} onChange={(event) => setShowDismissed(event.target.checked)} />
          <span>Show dismissed <em>{dismissedCount}</em></span>
        </label>
        <span className="pf-graph-toolbar-note">
          {layout.nodes.length} nodes{proposedCount > 0 ? ` · ${String(proposedCount)} to review` : ""}
        </span>
      </div>

      <div
        ref={viewport}
        className="pf-graph-viewport"
        tabIndex={0}
        role="group"
        aria-label="Plan graph. Arrow keys pan, plus and minus zoom, zero fits the view."
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onKeyDown={onKeyDown}
      >
        <svg className="pf-graph-svg" width="100%" height="100%" role="presentation">
          <defs>
            {MARKER_KINDS.map((marker) => (
              <marker
                key={marker.id}
                id={`pf-graph-arrow-${marker.id}`}
                viewBox="0 0 10 10"
                refX="9"
                refY="5"
                markerWidth="8"
                markerHeight="8"
                orient="auto"
                markerUnits="userSpaceOnUse"
              >
                <path className={`pf-graph-arrow ${marker.className}`} d="M0 1 L10 5 L0 9 z" />
              </marker>
            ))}
          </defs>
          <g ref={surface}>
            <g className="pf-graph-edges">
              {layout.edges.map((edge) => {
                const hover: HoverState =
                  neighborhood === null ? "normal" : neighborhood.edges.has(edge.key) ? "hl" : "dim";
                return <GraphEdgeView key={edge.key} edge={edge} hover={hover} />;
              })}
            </g>
            <g className="pf-graph-nodes">
              {layout.nodes.map((node) => {
                const hover: HoverState =
                  neighborhood === null ? "normal" : neighborhood.nodes.has(node.key) ? "hl" : "dim";
                return (
                  <GraphNodeView
                    key={node.key}
                    node={node}
                    hover={hover}
                    selected={selectedKey === node.key}
                    onActivate={activate}
                    onHover={hoverNode}
                    onReveal={reveal}
                    register={registerNode}
                  />
                );
              })}
            </g>
          </g>
        </svg>

        {layout.nodes.length === 0 ? (
          <div className="pf-graph-empty">Nothing to graph yet. Tasks and discoveries appear here.</div>
        ) : null}
      </div>

      <div className="pf-graph-legend" aria-hidden="true">
        <span><i className="pf-graph-swatch pf-graph-swatch--todo" />To do</span>
        <span><i className="pf-graph-swatch pf-graph-swatch--in_progress" />In progress</span>
        <span><i className="pf-graph-swatch pf-graph-swatch--done" />Done</span>
        <span><i className="pf-graph-swatch pf-graph-swatch--drifted" />Drifted</span>
        <span><i className="pf-graph-swatch pf-graph-swatch--proposed" />Proposed</span>
        <span><i className="pf-graph-swatch pf-graph-swatch--dismissed" />Dismissed</span>
      </div>

      <div className="pf-graph-controls" role="group" aria-label="Graph zoom">
        <button type="button" className="pf-btn pf-btn-ghost pf-btn-icon" onClick={() => zoomAt(0.8)} aria-label="Zoom out">
          <Minus size={14} aria-hidden="true" />
        </button>
        <span ref={zoomLabel} className="pf-graph-zoom" aria-live="off">100%</span>
        <button type="button" className="pf-btn pf-btn-ghost pf-btn-icon" onClick={() => zoomAt(1.25)} aria-label="Zoom in">
          <Plus size={14} aria-hidden="true" />
        </button>
        <button type="button" className="pf-btn pf-btn-ghost pf-btn-sm" onClick={fit} aria-label="Fit graph to screen">
          <Maximize2 size={13} aria-hidden="true" /> Fit
        </button>
      </div>

      {openDiscovery !== undefined ? (
        <DiscoveryPanel
          discovery={openDiscovery}
          nowMs={nowMs}
          pending={pending}
          onClose={closePanel}
          onAccept={accept}
          onDismiss={dismiss}
          onOpenParent={onOpenTask}
        />
      ) : null}
    </section>
  );
}
