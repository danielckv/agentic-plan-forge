import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { BoardApp } from "./board/BoardApp";
import { CenterApp } from "./center/CenterApp";
import { isCenterPath } from "./center/route";
import "./app.css";
import "./board/board.css";
import "./center/center.css";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("Plan Forge: #root missing from index.html");
}

// One bundle serves three apps, chosen by the path the server was asked for.
// The server returns index.html for any extensionless path, so a deep link to
// a command center route arrives here with that path intact.
function selectApp(): React.JSX.Element {
  const view = globalThis.PLAN_FORGE_VIEW;
  if (location.pathname === "/board" || view === "board") return <BoardApp />;
  if (isCenterPath(location.pathname) || view === "center") return <CenterApp />;
  return <App />;
}

createRoot(rootEl).render(<StrictMode>{selectApp()}</StrictMode>);
