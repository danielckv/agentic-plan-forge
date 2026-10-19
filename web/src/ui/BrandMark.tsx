/**
 * The Plan Forge mark: the pre-planning gate, drawn.
 *
 * Two bars narrow into a struck diamond — many inputs entering the gate, one
 * verified specification leaving it. That is the product in a glyph, and it
 * stays readable at 16px where an anvil or a hammer turns to mush.
 *
 * It is drawn rather than loaded as an image for three reasons. The bundle is
 * served under a Content-Security-Policy with `default-src 'self'`, so an
 * inline SVG needs no exception; it takes its colours from the theme tokens,
 * so light and dark need no second asset; and it stays crisp at every size the
 * UI uses. docs/logo.svg is the same geometry with the colours baked in, for
 * the README, where CSS variables do not resolve.
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

      {/* The billet the mark sits on. */}
      <rect width="32" height="32" rx="8" fill={`url(#${GRADIENT_ID})`} />

      {/* Two bars narrowing: what enters the gate. */}
      <g fill="var(--pf-accent-ink)">
        <rect x="7" y="7.8" width="18" height="3" rx="1.5" />
        <rect x="10.2" y="12.6" width="11.6" height="3" rx="1.5" />
        {/* The forged result. */}
        <path d="M16 17 20.2 21.2 16 25.4 11.8 21.2Z" />
      </g>
    </svg>
  );
}
