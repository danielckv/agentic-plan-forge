import { useMemo } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";

import { MermaidSvg } from "./MermaidSvg";

/**
 * Sanitizing markdown renderer configuration:
 * - `skipHtml` and no `rehype-raw`: raw HTML never renders;
 * - default `urlTransform`: URLs stay inert;
 * - `remark-gfm` + `rehype-sanitize`: GFM tables/lists, sanitized schema;
 * - links render inert with the visible URL; clicking opens a confirm dialog
 *   that calls `POST /api/open-url`;
 * - images render as alt text;
 * - Mermaid fenced blocks route through `MermaidSvg` (shared statement gate
 *   + DOMPurify SVG profile), never through dangerouslySetInnerHTML here;
 * - preview debounce is 120ms (handled by callers).
 */

export interface MarkdownViewProps {
  markdown: string;
  /** Confirm-then-launch handler for inert links (`POST /api/open-url`). */
  onOpenUrl?: (url: string) => void;
  className?: string;
}

/** `a` and `img` are removed from the sanitize allowlist; custom components render them inertly. */
const SANITIZE_SCHEMA = {
  ...defaultSchema,
  tagNames: (defaultSchema.tagNames ?? []).filter((t) => t !== "a" && t !== "img"),
};

export function MarkdownView({ markdown, onOpenUrl, className }: MarkdownViewProps): React.ReactElement {
  const components = useMemo(
    () => ({
      code(props: React.ComponentPropsWithoutRef<"code">): React.ReactElement {
        const { className: cls, children } = props;
        const text = String(children ?? "");
        if (cls === "language-mermaid") {
          return <MermaidSvg source={text} />;
        }
        return <code className={cls}>{children}</code>;
      },
      // Links render inert with the visible URL; a click asks first.
      a(props: React.ComponentPropsWithoutRef<"a">): React.ReactElement {
        const { href, children } = props;
        const url = href ?? "";
        return (
          <span>
            <button
              type="button"
              className="pf-inert-link"
              style={{ textDecoration: "underline", cursor: "pointer" }}
              onClick={() => onOpenUrl?.(url)}
            >
              {children}
            </button>{" "}
            <small style={{ color: "var(--pf-muted)" }}>({url})</small>
          </span>
        );
      },
      // Images render as alt text; nothing is fetched.
      img(props: React.ComponentPropsWithoutRef<"img">): React.ReactElement {
        const { alt } = props;
        return <em>[image: {alt ?? "unnamed"}]</em>;
      },
    }),
    [onOpenUrl],
  );

  return (
    <div className={className}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[[rehypeSanitize, SANITIZE_SCHEMA]]}
        skipHtml
        components={components}
      >
        {markdown}
      </ReactMarkdown>
    </div>
  );
}
