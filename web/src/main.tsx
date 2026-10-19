import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { BoardApp } from "./board/BoardApp";
import "./app.css";
import "./board/board.css";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("Plan Forge: #root missing from index.html");
}

createRoot(rootEl).render(
  <StrictMode>
    {location.pathname === "/board" || globalThis.PLAN_FORGE_VIEW === "board" ? (
      <BoardApp />
    ) : (
      <App />
    )}
  </StrictMode>,
);
