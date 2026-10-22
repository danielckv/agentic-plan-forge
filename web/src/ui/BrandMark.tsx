/**
 * The Plan Forge mark: a "P" whose bowl closes into a ring — the gate a plan
 * passes through — and a spark beside it for the specification that comes
 * out. It stays readable at 16px.
 *
 * It is drawn rather than loaded as an image for three reasons. The bundle is
 * served under a Content-Security-Policy with `default-src 'self'`, so an
 * inline SVG needs no exception; it takes its colours from the theme tokens,
 * so light and dark need no second asset; and it stays crisp at every size the
 * UI uses. docs/logo.svg and web/public/favicon.svg are the same geometry with
 * the colours baked in, for places where CSS variables do not resolve.
 */

const GRADIENT_ID = "pf-brand-grad";

export function BrandMark({
  size = 28,
  title,
}: {
  size?: number;
  /** Supply to expose the mark to assistive technology; omit to hide it. */
  title?: string;
}): React.JSX.Element {
  return (
    <svg
      className="pf-brandmark"
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      <defs>
        <linearGradient id={GRADIENT_ID} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="var(--pf-accent-hover)" />
          <stop offset="1" stopColor="var(--pf-epic)" />
        </linearGradient>
      </defs>

      <rect width="32" height="32" rx="8" fill={`url(#${GRADIENT_ID})`} />

      <g transform="translate(-1.4 0)">
        {/* The P: its bowl is a closed ring, the gate. */}
        <path
          d="M11 24.5V7.75h5.75a5.5 5.5 0 0 1 0 11H11"
          fill="none"
          stroke="var(--pf-accent-ink)"
          strokeWidth="3.3"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        {/* The spark: the verified specification. */}
        <path
          d="M23.6 19.4l.95 2.15 2.15.95-2.15.95-.95 2.15-.95-2.15-2.15-.95 2.15-.95z"
          fill="var(--pf-accent-ink)"
        />
      </g>
    </svg>
  );
}
