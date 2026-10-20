/**
 * Setup.
 *
 * It explains the local surface and nothing else: there are no tracker
 * credentials to collect, because the command center makes no outbound
 * request. Anything that writes to disk stays a terminal command, so this page
 * never asks the browser to install a hook behind the user's back.
 */
export function SetupPage(): React.JSX.Element {
  const steps: { title: string; body: string; command?: string }[] = [
    {
      title: "Install the hooks",
      body: "Registers the skills and hooks for every agent found on this machine, and refreshes them with the current binary path.",
      command: "plan-forge install --host auto",
    },
    {
      title: "Check what is wired",
      body: "Reports which agents were found, whether each is signed in, and which skill roots they read.",
      command: "plan-forge doctor",
    },
    {
      title: "Change the model slots and workday",
      body: "Sets the main and critic host and model pair, and the productive hours a day that estimates assume.",
      command: "plan-forge config",
    },
    {
      title: "Open this dashboard",
      body: "Starts the command center if it is not running, then opens a tab on it. Starting an agent brings the daemon up but opens nothing, so this is how you get here.",
      command: "plan-forge center open",
    },
  ];

  return (
    <>
      <div className="pf-center-pagehead">
        <h1 className="pf-h2">Setup</h1>
        <p className="pf-caption">Everything here runs on this machine.</p>
      </div>

      <section className="pf-center-card">
        <h2 className="pf-h3">Local only</h2>
        <p className="pf-body">
          The command center listens on loopback and makes no outbound request. It stores no
          credentials and connects to no issue tracker: the plan, the board and the live sessions
          are all local state, and the only thing that leaves your machine is what you choose to
          share from a remote forge session.
        </p>
      </section>

      <h2 className="pf-center-section">Commands</h2>
      <ol className="pf-center-steps">
        {steps.map((step, i) => (
          <li key={step.title} className="pf-center-card pf-center-step">
            <span className="pf-center-step-n" aria-hidden="true">
              {i + 1}
            </span>
            <div>
              <h3 className="pf-center-step-title">{step.title}</h3>
              <p className="pf-body">{step.body}</p>
              {step.command ? <pre className="pf-center-code">{step.command}</pre> : null}
            </div>
          </li>
        ))}
      </ol>
    </>
  );
}
