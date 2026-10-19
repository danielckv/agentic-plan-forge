import { Dialog } from "./Dialog";
import { SHORTCUT_HELP } from "./shortcuts";

/** The `?` overlay: every shortcut the window answers to. */
export function ShortcutHelp(props: { open: boolean; onClose: () => void }): React.ReactElement | null {
  return (
    <Dialog
      open={props.open}
      onClose={props.onClose}
      title="Keyboard shortcuts"
      description="Single-key shortcuts pause while you type in a field."
    >
      <dl className="pf-shortcuts">
        {SHORTCUT_HELP.map((s) => (
          <div key={s.label} className="pf-shortcut-row">
            <dt>
              {s.label}
              {s.scope !== undefined && <span className="pf-caption"> · {s.scope}</span>}
            </dt>
            <dd>
              {s.keys.map((k, i) => (
                <span key={k}>
                  {i > 0 && <span className="pf-caption"> or </span>}
                  <kbd className="pf-kbd">{k}</kbd>
                </span>
              ))}
            </dd>
          </div>
        ))}
      </dl>
    </Dialog>
  );
}
