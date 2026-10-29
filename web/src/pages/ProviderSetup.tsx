import { CircleCheck, CircleSlash, KeyRound } from "lucide-react";
import { useState } from "react";

import { saveProvider } from "../ui/capabilities";
import type { ProviderStatus } from "../ui/capabilities";

/**
 * API provider setup: the keys that let a plan run over HTTP instead of
 * through a local agent CLI.
 *
 * The key is typed here by the owner and goes straight to the loopback bridge,
 * which writes it to a 0600 file outside `config.json`. It is never read back:
 * once saved, the window only ever sees the masked form, so nothing can lift a
 * key out of this screen or out of a screenshot of it.
 *
 * This is deliberately not a sign-in. A Claude, ChatGPT or other consumer
 * subscription cannot be used by a third-party tool, and Plan Forge does not
 * ask for one: a subscription is reached only by running that vendor's own
 * CLI, which stays available as a separate host.
 */
export function ProviderSetup(props: {
  providers: ProviderStatus[];
  onSaved: () => void | Promise<void>;
}): React.ReactElement {
  const { providers, onSaved } = props;
  return (
    <section className="pf-cap-section">
      <h3 className="pf-cap-title">
        <KeyRound size={13} /> API providers
      </h3>
      <p className="pf-caption">
        A key here runs plans over the provider&apos;s API: no process to start per call, the
        prompt prefix is cached between stages, and replies are constrained to the reply shape.
        Leave them all empty to keep using the local agent CLIs below.
      </p>
      <ul className="pf-cap-list">
        {providers.map((provider) => (
          <ProviderRow key={provider.name} provider={provider} onSaved={onSaved} />
        ))}
      </ul>
    </section>
  );
}

function ProviderRow(props: {
  provider: ProviderStatus;
  onSaved: () => void | Promise<void>;
}): React.ReactElement {
  const { provider, onSaved } = props;
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A key supplied through the environment is not ours to edit: the file we
  // would write is already being overridden, so saying so beats silently
  // storing a key that never takes effect.
  const fromEnvironment = provider.configured && provider.source === provider.env_var;

  async function submit(next: string): Promise<void> {
    setBusy(true);
    setError(null);
    const failure = await saveProvider(provider.name, next, provider.base_url ?? "");
    setBusy(false);
    if (failure !== null) {
      setError(failure);
      return;
    }
    setKey("");
    await onSaved();
  }

  return (
    <li className="pf-cap-row" data-tone={provider.configured ? "serves" : "optional"}>
      <span className="pf-cap-mark" aria-hidden="true">
        {provider.configured ? <CircleCheck size={15} /> : <CircleSlash size={15} />}
      </span>
      <span className="pf-cap-name">{provider.label}</span>
      <span className="pf-cap-state">{provider.configured ? "Connected" : "No key"}</span>
      <span className="pf-cap-detail pf-caption">
        {fromEnvironment ? (
          <>
            Key from <code className="pf-mono">{provider.env_var}</code> ({provider.masked}). Unset
            that variable to manage the key here.
          </>
        ) : (
          <>
            {provider.configured && (
              <>
                Stored key <code className="pf-mono">{provider.masked}</code>.{" "}
              </>
            )}
            <label className="pf-sr-only" htmlFor={`pf-key-${provider.name}`}>
              {provider.label} API key
            </label>
            <input
              id={`pf-key-${provider.name}`}
              className="pf-input pf-mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder={provider.configured ? "Replace key…" : "Paste API key…"}
              value={key}
              disabled={busy}
              onChange={(e) => setKey(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && key.trim() !== "") void submit(key.trim());
              }}
            />
            <button
              type="button"
              className="pf-btn pf-btn-sm"
              disabled={busy || key.trim() === ""}
              onClick={() => void submit(key.trim())}
            >
              Save
            </button>
            {provider.configured && (
              <button
                type="button"
                className="pf-btn pf-btn-sm"
                disabled={busy}
                onClick={() => void submit("")}
              >
                Remove
              </button>
            )}
            <br />
            Create one at <code className="pf-mono">{provider.console_url}</code>, or export{" "}
            <code className="pf-mono">{provider.env_var}</code>.
          </>
        )}
        {error !== null && (
          <span className="pf-banner pf-banner--danger" role="status">
            {error}
          </span>
        )}
      </span>
    </li>
  );
}
