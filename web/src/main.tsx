import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "./app.css";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("Plan-Forge: #root missing from index.html");
}

createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
