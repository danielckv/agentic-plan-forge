/**
 * Skeleton loaders for content that is pending, so a page never reads as
 * empty or broken while the authority is still answering. Decorative only:
 * they are `aria-hidden`, and the surrounding region carries `aria-busy`.
 */

/** Default line widths vary so a stack reads as text, not as a barcode. */
const LINE_WIDTHS = ["92%", "78%", "86%", "64%", "72%"];

export function SkeletonLines(props: { lines?: number; className?: string }): React.ReactElement {
  const { lines = 3, className } = props;
  return (
    <div className={`pf-skeleton-stack ${className ?? ""}`} aria-hidden="true">
      {Array.from({ length: lines }, (_, i) => (
        <span key={i} className="pf-skeleton" style={{ width: LINE_WIDTHS[i % LINE_WIDTHS.length] }} />
      ))}
    </div>
  );
}

/** A card-shaped placeholder: heading bar plus text lines. */
export function SkeletonCard(props: { lines?: number }): React.ReactElement {
  return (
    <div className="pf-card pf-card-pad pf-skeleton-card" aria-hidden="true">
      <span className="pf-skeleton pf-skeleton--heading" />
      <SkeletonLines lines={props.lines ?? 3} />
    </div>
  );
}

/** Placeholder for a status chip while the first poll is in flight. */
export function SkeletonChip(props: { width?: number }): React.ReactElement {
  return <span className="pf-skeleton pf-skeleton--chip" style={{ width: props.width ?? 120 }} aria-hidden="true" />;
}
