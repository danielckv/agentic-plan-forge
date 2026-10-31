import { useEffect, useId, useState } from "react";
import DOMPurify from "dompurify";
import mermaid from "mermaid";
import { Code, Maximize2 } from "lucide-react";

import { mermaidSourceRejected } from "../protocol/schema";
import { Collapse } from "../ui/Collapse";
import { Dialog } from "../ui/Dialog";
import { prefs, useEffectiveTheme } from "../ui/prefs";
import type { EffectiveTheme } from "../ui/prefs";
import { SkeletonLines } from "../ui/Skeleton";

/**
 * The ONLY component in the tree allowed to use dangerouslySetInnerHTML
 * (eslint react/no-danger is disabled for this file alone). Every diagram
 * source from any origin — live drafts, the relay, or `plan-forge open` of a
 * committed spec — passes the shared statement gate, `mermaid.parse`, and a
 * DOMPurify SVG profile before insertion. Anything that survives is inert.
 */

type MermaidTheme = "dark" | "neutral";

/**
 * Mermaid bakes colors into the SVG at render time, so a diagram cannot follow
 * the window theme through CSS. The effective theme (the Theme selector, or
 * the OS when set to System) picks the Mermaid theme, and a change
 * re-initializes Mermaid and re-renders every visible diagram.
 */
function mermaidThemeFor(effective: EffectiveTheme): MermaidTheme {
  return effective === "dark" ? "dark" : "neutral";
}

let appliedTheme: MermaidTheme | null = null;

/**
 * Initialize Mermaid for `effective`, only when that changes the theme.
 * Returns true when it re-initialized. Constants only: never pass user or LLM
 * data to `initialize`.
 */
function ensureMermaidTheme(effective: EffectiveTheme): boolean {
  const theme = mermaidThemeFor(effective);
  if (theme === appliedTheme) return false;
  appliedTheme = theme;
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    suppressErrorRendering: true,
    htmlLabels: false,
    maxTextSize: 50000,
    maxEdges: 500,
    theme,
  });
  return true;
}

ensureMermaidTheme(prefs.getSnapshot().effectiveTheme);

interface MermaidSvgProps {
  source: string;
  /** Optional caption rendered above the diagram. */
  title?: string;
}

interface DiagramState {
  svg: string | null;
  diagnostic: string | null;
  rawSource: string;
  /** The id Mermaid rendered `svg` under; every internal id derives from it. */
  renderId?: string;
}

/**
 * Re-key every id inside a Mermaid SVG. Mermaid scopes its ids, `<style>`
 * selectors, marker `url(#…)` references and aria links with the render id, so
 * one global substitution yields a second, self-consistent copy. The expanded
 * modal shows that copy: two live copies of one SVG would otherwise duplicate
 * ids and let CSS or `url(#…)` references resolve to the wrong element.
 */
export function reidentifySvg(svg: string, fromId: string, toId: string): string {
  if (fromId === "" || fromId === toId) return svg;
  return svg.split(fromId).join(toId);
}

/**
 * Shared render pipeline: pre-parse source gate -> mermaid.parse ->
 * mermaid.render -> DOMPurify SVG profile. On any rejection the component
 * shows the source in a code block with a diagnostic, never a broken frame.
 * `effective` defaults to the current window theme; Mermaid is re-initialized
 * for it immediately before the render so a theme switch never renders stale.
 */
export function renderDiagram(
  source: string,
  effective: EffectiveTheme = prefs.getSnapshot().effectiveTheme,
): Promise<DiagramState> {
  const gate = mermaidSourceRejected(source);
  if (gate !== null) {
    return Promise.resolve({ svg: null, diagnostic: `Mermaid rejected: ${gate}`, rawSource: source });
  }
  const renderId = `pf-mmd-${crypto.randomUUID()}`;
  return mermaid
    .parse(source, { suppressErrors: true })
    .then((parsed: unknown) => {
      // With suppressErrors, an invalid diagram resolves to `false` instead of
      // throwing, so the gate must test the value.
      if (parsed === false) throw new Error("the diagram source did not parse");
      ensureMermaidTheme(effective);
      return mermaid.render(renderId, source);
    })
    .then(({ svg }: { svg: string }) => ({
      svg: DOMPurify.sanitize(svg, {
        USE_PROFILES: { svg: true },
        FORBID_TAGS: ["foreignObject", "script", "a"],
        FORBID_ATTR: ["href", "xlink:href"],
      }),
      diagnostic: null,
      rawSource: source,
      renderId,
    }))
    .catch((err: unknown) => ({
      svg: null,
      diagnostic: `Mermaid parse error: ${err instanceof Error ? err.message : String(err)}`,
      rawSource: source,
    }));
}

export function MermaidSvg({ source, title }: MermaidSvgProps): React.ReactElement {
  const theme = useEffectiveTheme();
  const [diagram, setDiagram] = useState<DiagramState | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [showSource, setShowSource] = useState(false);
  const captionId = useId();
  const sourceId = useId();

  // Re-runs on a new source and on a theme change. The previous SVG stays on
  // screen until the new one lands, so a switch does not flash empty frames.
  useEffect(() => {
    let cancelled = false;
    void renderDiagram(source, theme).then((next) => {
      if (!cancelled) setDiagram(next);
    });
    return () => {
      cancelled = true;
    };
  }, [source, theme]);

  const heading = title ?? "Diagram";

  if (diagram === null) {
    return (
      <div className="pf-diagram-card" aria-busy="true">
        <div className="pf-diagram-bar">
          <span className="pf-diagram-title">{heading}</span>
        </div>
        <figure className="pf-mermaid pf-mermaid--pending" aria-label={`${heading} (rendering)`}>
          <SkeletonLines lines={4} />
        </figure>
      </div>
    );
  }

  if (diagram.svg === null) {
    return (
      <div className="pf-diagram-card">
        <div className="pf-diagram-bar">
          <span className="pf-diagram-title">{heading}</span>
        </div>
        <figure className="pf-mermaid" aria-label={heading}>
          <p className="pf-diagram-error" role="alert">
            {diagram.diagnostic ?? "diagram unavailable"}
          </p>
          <pre className="pf-diagram-source">
            <code>{diagram.rawSource}</code>
          </pre>
        </figure>
      </div>
    );
  }

  return (
    <div className="pf-diagram-card">
      <div className="pf-diagram-bar">
        <span id={captionId} className="pf-diagram-title">
          {heading}
        </span>
        <div className="pf-diagram-actions">
          <button
            type="button"
            className="pf-btn pf-btn-ghost pf-btn-sm"
            aria-label={showSource ? "Hide source" : "View source"}
            title={showSource ? "Hide source" : "View source"}
            aria-expanded={showSource}
            aria-controls={sourceId}
            onClick={() => setShowSource((v) => !v)}
          >
            <Code size={13} />
            <span className="pf-btn-label">{showSource ? "Hide source" : "View source"}</span>
          </button>
          <button
            type="button"
            className="pf-btn pf-btn-ghost pf-btn-sm"
            aria-label="Expand"
            title="Expand diagram"
            onClick={() => setExpanded(true)}
          >
            <Maximize2 size={13} />
            <span className="pf-btn-label">Expand</span>
          </button>
        </div>
      </div>
      <figure className="pf-mermaid" aria-labelledby={captionId}>
        {/* Sanitized inline SVG: the only dangerouslySetInnerHTML in the tree. */}
        <div className="pf-mermaid-stage" dangerouslySetInnerHTML={{ __html: diagram.svg }} />
      </figure>
      <Collapse id={sourceId} open={showSource}>
        <pre className="pf-diagram-source pf-scroll-thin">
          <code>{diagram.rawSource}</code>
        </pre>
      </Collapse>
      <Dialog open={expanded} onClose={() => setExpanded(false)} title={heading} size="wide">
        <figure className="pf-mermaid pf-mermaid--expanded" aria-label={`${heading}, expanded`}>
          <div
            className="pf-mermaid-stage"
            dangerouslySetInnerHTML={{
              __html: reidentifySvg(diagram.svg, diagram.renderId ?? "", `${diagram.renderId ?? ""}-expanded`),
            }}
          />
        </figure>
      </Dialog>
    </div>
  );
}
