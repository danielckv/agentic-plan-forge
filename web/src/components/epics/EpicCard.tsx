import { FileText } from "lucide-react";

/**
 * One epic shown as an attached file: an icon, its file name, the title and
 * goal, a few counts, and the code it touches as chips. Clicking anywhere on
 * the card opens it. Page 1 shows the draft epics this way, and Page 2 the
 * Product Manager's.
 */
export interface EpicCardProps {
  id: string;
  title: string;
  goal?: string;
  filename: string;
  /** Counts with a singular and plural label, such as ["requirement", "requirements"]. */
  stats: Array<{ label: [string, string]; value: number }>;
  /** Component names, shown as chips; at most four are shown. */
  tags: string[];
  onOpen: () => void;
}

const MAX_TAGS = 4;

export function EpicCard({ id, title, goal, filename, stats, tags, onOpen }: EpicCardProps): React.ReactElement {
  const shown = tags.slice(0, MAX_TAGS);
  const more = tags.length - shown.length;
  return (
    <button type="button" className="pf-epic-card" onClick={onOpen} aria-label={`Open ${id}: ${title}`}>
      <span className="pf-epic-card-icon" aria-hidden="true">
        <FileText size={16} />
      </span>
      <span className="pf-epic-card-main">
        <span className="pf-epic-card-file">{filename}</span>
        <span className="pf-epic-card-title">{title}</span>
        {goal !== undefined && goal !== "" && <span className="pf-epic-card-goal">{goal}</span>}
        <span className="pf-epic-card-meta">
          {stats.map((s) => (
            <span key={s.label[1]}>
              <strong>{s.value}</strong> {s.value === 1 ? s.label[0] : s.label[1]}
            </span>
          ))}
        </span>
        {shown.length > 0 && (
          <span className="pf-epic-card-tags">
            {shown.map((tag) => (
              <code key={tag} className="pf-tag">
                {tag}
              </code>
            ))}
            {more > 0 && <span className="pf-tag pf-tag--more">+{more}</span>}
          </span>
        )}
      </span>
    </button>
  );
}
