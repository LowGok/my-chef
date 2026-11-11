import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./style.css";
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    void (async () => {
      // Retire only the former MyChef root worker; the landing page stays outside
      // the application's offline scope.
      const registrations = await navigator.serviceWorker.getRegistrations();
      for (const registration of registrations) {
        const worker =
          registration.active ??
          registration.waiting ??
          registration.installing;
        if (
          registration.scope === new URL("/", location.origin).href &&
          worker &&
          new URL(worker.scriptURL).pathname === "/sw.js"
        )
          await registration.unregister();
      }
      await navigator.serviceWorker.register("/app/sw.js", { scope: "/app/" });
    })().catch(() => {
      // Shopping remains connected-only when the browser cannot install a shell.
    });
  });
}
