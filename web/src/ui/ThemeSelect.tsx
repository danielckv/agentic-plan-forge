import { Monitor, Moon, Sun } from "lucide-react";

import { prefs, THEME_ORDER, usePrefs } from "./prefs";
import type { ThemeChoice } from "./prefs";

const THEME_META: Record<ThemeChoice, { label: string; icon: React.ComponentType<{ size?: number | string }> }> = {
  system: { label: "System", icon: Monitor },
  light: { label: "Light", icon: Sun },
  dark: { label: "Dark", icon: Moon },
};

/**
 * System / Light / Dark as a segmented control. The compact variant, in the
 * top bar, shows icons only; each button keeps its name for assistive
 * technology and as a tooltip. Both set `data-theme` on `<html>` through the
 * prefs store and persist to `plan-forge:ui:theme`.
 */
export function ThemeSelect({ variant = "full" }: { variant?: "full" | "compact" }): React.ReactElement {
  const { theme } = usePrefs();
  const compact = variant === "compact";
  return (
    <div className={compact ? "pf-segmented pf-segmented--compact" : "pf-segmented"} role="group" aria-label="Theme">
      {THEME_ORDER.map((choice) => {
        const { label, icon: Icon } = THEME_META[choice];
        return (
          <button
            key={choice}
            type="button"
            className="pf-segment"
            aria-pressed={theme === choice}
            aria-label={label}
            title={compact ? `${label} theme` : undefined}
            onClick={() => prefs.setTheme(choice)}
          >
            <Icon size={compact ? 14 : 13} />
            {!compact && label}
          </button>
        );
      })}
    </div>
  );
}
