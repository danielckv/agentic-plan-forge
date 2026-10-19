import { useState } from "react";

/**
 * Animated disclosure region. Height animates through a CSS grid row
 * (`0fr` -> `1fr`), which needs no measurement and follows content that
 * changes size mid-flight. While closed the region is `inert` and
 * `visibility: hidden`, so its controls leave the tab order and the
 * accessibility tree.
 *
 * Content mounts the first time the region opens and stays mounted after,
 * so closing never discards state (and heavy children, like a rendered
 * diagram, cost nothing until the user asks for them).
 */
export function Collapse(props: {
  open: boolean;
  id?: string;
  className?: string;
  children: React.ReactNode;
}): React.ReactElement {
  const { open, id, className, children } = props;
  const [everOpened, setEverOpened] = useState(open);
  if (open && !everOpened) setEverOpened(true);

  return (
    <div
      id={id}
      className={className === undefined ? "pf-collapse" : `pf-collapse ${className}`}
      data-open={open}
      inert={!open}
    >
      <div className="pf-collapse-inner">{everOpened ? children : null}</div>
    </div>
  );
}
