import { Suspense, lazy, useEffect, useMemo, useRef, useState } from "react";
import { Maximize2 } from "lucide-react";

import { buildPlanGraph, newNodeIds } from "../ui/planGraph";
import type { PlanGraph } from "../ui/planGraph";
import { hasWebGL } from "../ui/webgl";
import { Dialog } from "../ui/Dialog";
import { PlanTree } from "./PlanTree";
import type { ArchitectDraft, PMDraft } from "../protocol/drafts";
import type { CritiqueIssue } from "../store/syncStore";

/**
 * three.js is reached only through this import, so the bundler gives it its
 * own chunk that is fetched when the view first opens. The UI is compiled into
 * the Go binary, so an unconditional import would add its weight to every
 * install whether or not anyone opens the graph.
 */
const PlanGraph3D = lazy(() => import("./PlanGraph3D"));

/** How long a newly drafted node stays highlighted. */
const FRESH_MS = 6000;

/**
 * The plan graph, in 3D where that is possible and as a tree where it is not.
 *
 * Both render from the same `PlanGraph`, so they can never disagree about what
 * the plan contains — the fallback is a different presentation, not a reduced
 * one. Either way the view can be opened full screen: the rail is too narrow
 * to read a plan in, which is what the expand control is for.
 */
export function PlanGraphView(props: {
  pm: PMDraft | null;
  architect: ArchitectDraft | null;
  critiques: CritiqueIssue[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}): React.ReactElement {
  const { pm, architect, critiques, selectedId, onSelect } = props;
  const graph = useMemo(() => buildPlanGraph(pm, architect, critiques), [pm, architect, critiques]);
  const fresh = useFreshNodes(graph);
  const [expanded, setExpanded] = useState(false);

  // Decided once per mount. The answer cannot change while the view is open,
  // and probing creates a real context.
  const [webgl] = useState(() => hasWebGL());

  const render = (full: boolean): React.ReactElement => {
    if (!webgl) {
      return <PlanTree graph={graph} fresh={fresh} selectedId={selectedId} onSelect={onSelect} />;
    }
    return (
      <Suspense fallback={<PlanTree graph={graph} fresh={fresh} selectedId={selectedId} onSelect={onSelect} />}>
        <PlanGraph3D
          graph={graph}
          fresh={fresh}
          selectedId={selectedId}
          onSelect={onSelect}
          expanded={full}
        />
      </Suspense>
    );
  };

  return (
    <div className="pf-plangraph">
      <div className="pf-plangraph-bar">
        {!webgl && (
          <p className="pf-caption">
            This browser has no WebGL, so the plan is shown as a list of documents. It carries the same nodes.
          </p>
        )}
        <button
          type="button"
          className="pf-btn pf-btn-ghost pf-btn-sm pf-plangraph-expand"
          aria-label="Open the plan full screen"
          title="Open the plan full screen"
          onClick={() => setExpanded(true)}
        >
          <Maximize2 size={13} /> Expand
        </button>
      </div>
      {render(false)}
      <Dialog
        open={expanded}
        onClose={() => setExpanded(false)}
        title="Plan"
        description={
          webgl
            ? "Drag to orbit, scroll to zoom, click a node for its details."
            : "Select a document to read its details."
        }
        size="full"
      >
        <div className="pf-plangraph pf-plangraph--full">{render(true)}</div>
      </Dialog>
    </div>
  );
}

/**
 * useFreshNodes reports which nodes arrived in the most recent draft.
 *
 * A draft frame carries the whole document and replaces the one before it, so
 * nothing on the wire says "this epic is new". Comparing one graph with the
 * last is what turns a replaced document into visible growth, and it is why
 * this view needs no new event from the backend.
 */
function useFreshNodes(graph: PlanGraph): ReadonlySet<string> {
  const previous = useRef<PlanGraph | null>(null);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(() => new Set());

  useEffect(() => {
    const before = previous.current;
    previous.current = graph;
    // The first graph a session sees is entirely "new". Highlighting it would
    // light up the whole plan for someone who joined after the run, so only
    // growth from a graph we already showed counts.
    if (before === null) return;
    const added = newNodeIds(before, graph);
    if (added.length === 0) return;
    setFresh(new Set(added));
    const timer = setTimeout(() => setFresh(new Set()), FRESH_MS);
    return () => clearTimeout(timer);
  }, [graph]);

  return fresh;
}
