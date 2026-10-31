import { useEffect, useState } from "react";

import {
  fetchComponents,
  fetchImpact,
  fetchPlanFiles,
  fetchProjectContext,
  type ComponentsView,
  type ImpactMap,
  type PlanFile,
  type ProjectContext,
} from "../../protocol/api";

/**
 * Hooks over the read-only planning endpoints. Each refetches when its key
 * changes (an interpretation revision, a context pill, a finished scan) and
 * keeps the last good answer when a fetch fails, so a blip never empties the
 * rail.
 */

interface PlanFilesState {
  files: PlanFile[];
  revision: number;
  loaded: boolean;
}

/**
 * The draft epics, refetched whenever the interpretation or the impact map
 * moves. Both matter: the epics change with the interpretation, and their
 * links change again when the model's map replaces the keyword ranking under
 * the same interpretation.
 */
export function usePlanFiles(revision: number, impactRevision = 0): PlanFilesState {
  const [state, setState] = useState<PlanFilesState>({ files: [], revision: 0, loaded: false });
  useEffect(() => {
    if (revision === 0) return;
    let live = true;
    fetchPlanFiles().then(
      (v) => {
        if (live) setState({ files: v.files, revision: v.revision, loaded: true });
      },
      () => {
        if (live) setState((s) => ({ ...s, loaded: true }));
      },
    );
    return () => {
      live = false;
    };
  }, [revision, impactRevision]);
  return state;
}

/**
 * The impact map, refetched whenever a new one is published. Returns null
 * until the first answer, which the panel renders as its pending state.
 */
export function useImpact(impactRevision: number): ImpactMap | null {
  const [map, setMap] = useState<ImpactMap | null>(null);
  useEffect(() => {
    if (impactRevision === 0) return;
    let live = true;
    fetchImpact().then(
      (v) => {
        if (live) setMap(v);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [impactRevision]);
  return map;
}

/** The project context, refetched whenever the context pill changes. */
export function useProjectContext(key: string): ProjectContext | null {
  const [ctx, setCtx] = useState<ProjectContext | null>(null);
  useEffect(() => {
    if (key === "") return;
    let live = true;
    fetchProjectContext().then(
      (v) => {
        if (live) setCtx(v);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [key]);
  return ctx;
}

/** The repository's blocks, once the scan has produced a graph. */
export function useComponentBlocks(scanDone: boolean): ComponentsView | null {
  const [view, setView] = useState<ComponentsView | null>(null);
  useEffect(() => {
    if (!scanDone) return;
    let live = true;
    fetchComponents().then(
      (v) => {
        if (live) setView(v);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [scanDone]);
  return view;
}
