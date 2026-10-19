import { Monitor, Moon, Sun } from "lucide-react";

import { prefs, THEME_ORDER, usePrefs } from "./prefs";
import type { ThemeChoice } from "./prefs";

const THEME_META: Record<ThemeChoice, { label: string; icon: React.ComponentType<{ size?: number | string }> }> = {
  system: { label: "System", icon: Monitor },
  light: { label: "Light", icon: Sun },
  dark: { label: "Dark", icon: Moon },
};

/**
 * System / Light / Dark. Expanded, a segmented control; in the collapsed rail
 * a single button that cycles through the three. Both set `data-theme` on
 * `<html>` through the prefs store and persist to `plan-forge:ui:theme`.
 */
export function ThemeSelect({ collapsed }: { collapsed: boolean }): React.ReactElement {
  const { theme } = usePrefs();

  if (collapsed) {
    const meta = THEME_META[theme];
    const Icon = meta.icon;
    return (
      <button
        type="button"
        className="pf-rail-btn"
        aria-label={`Theme: ${meta.label}. Activate to change.`}
        title={`Theme: ${meta.label}`}
        onClick={prefs.cycleTheme}
      >
        <Icon size={15} />
      </button>
    );
  }

  return (
    <div className="pf-segmented" role="group" aria-label="Theme">
      {THEME_ORDER.map((choice) => {
        const { label, icon: Icon } = THEME_META[choice];
        return (
          <button
            key={choice}
            type="button"
            className="pf-segment"
            aria-pressed={theme === choice}
            onClick={() => prefs.setTheme(choice)}
          >
            <Icon size={13} />
            {label}
          </button>
        );
      })}
    </div>
  );
}
