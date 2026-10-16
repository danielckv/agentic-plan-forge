import { useEffect, useState } from "react";
import DOMPurify from "dompurify";
import mermaid from "mermaid";

import { mermaidSourceRejected } from "../protocol/schema";

/**
 * The ONLY component in the tree allowed to use dangerouslySetInnerHTML
 * (eslint react/no-danger is disabled for this file alone). Every diagram
 * source from any origin — live drafts, the relay, or `plan-forge open` of a
 * committed spec — passes the shared statement gate, `mermaid.parse`, and a
 * DOMPurify SVG profile before insertion. Anything that survives is inert.
 */

// Initialize once from constants; never pass user/LLM data here.
mermaid.initialize({
  startOnLoad: false,
  securityLevel: "strict",
  suppressErrorRendering: true,
  htmlLabels: false,
  maxTextSize: 50000,
  maxEdges: 500,
});

export interface MermaidSvgProps {
  source: string;
  /** Optional caption rendered above the diagram. */
  title?: string;
}

export interface DiagramState {
  svg: string | null;
  diagnostic: string | null;
  rawSource: string;
}

/**
 * Shared render pipeline: pre-parse source gate -> mermaid.parse ->
 * mermaid.render -> DOMPurify SVG profile. On any rejection the component
 * shows the source in a code block with a diagnostic, never a broken frame.
 */
export function renderDiagram(source: string): Promise<DiagramState> {
  const gate = mermaidSourceRejected(source);
  if (gate !== null) {
    return Promise.resolve({ svg: null, diagnostic: `Mermaid rejected: ${gate}`, rawSource: source });
  }
  return mermaid
    .parse(source, { suppressErrors: true })
    .then(() => mermaid.render(`pf-mmd-${crypto.randomUUID()}`, source))
    .then(({ svg }: { svg: string }) => ({
      svg: DOMPurify.sanitize(svg, {
        USE_PROFILES: { svg: true },
        FORBID_TAGS: ["foreignObject", "script", "a"],
        FORBID_ATTR: ["href", "xlink:href"],
      }),
      diagnostic: null,
      rawSource: source,
    }))
    .catch((err: unknown) => ({
      svg: null,
      diagnostic: `Mermaid parse error: ${err instanceof Error ? err.message : String(err)}`,
      rawSource: source,
    }));
}

export function MermaidSvg({ source, title }: MermaidSvgProps): React.ReactElement {
  const [diagram, setDiagram] = useState<DiagramState>({
    svg: null,
    diagnostic: null,
    rawSource: source,
  });

  useEffect(() => {
    let cancelled = false;
    void renderDiagram(source).then((next) => {
      if (!cancelled) setDiagram(next);
    });
    return () => {
      cancelled = true;
    };
  }, [source]);

  if (diagram.svg === null) {
    return (
      <figure className="pf-mermaid">
        {title !== undefined && <figcaption className="text-sm font-semibold">{title}</figcaption>}
        <p className="text-sm" style={{ color: "var(--pf-danger)" }} role="alert">
          {diagram.diagnostic ?? "diagram unavailable"}
        </p>
        <pre>
          <code>{diagram.rawSource}</code>
        </pre>
      </figure>
    );
  }
  return (
    <figure className="pf-mermaid">
      {title !== undefined && <figcaption className="text-sm font-semibold">{title}</figcaption>}
      {/* Sanitized inline SVG: the only dangerouslySetInnerHTML in the tree. */}
      <div dangerouslySetInnerHTML={{ __html: diagram.svg }} />
    </figure>
  );
}
