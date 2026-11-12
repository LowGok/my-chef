import React from "react";
import { createRoot } from "react-dom/client";
import {
  initializeNativePlatform,
  NativeStartupError,
} from "./native-platform";
import { installNativeLinkHandling } from "./platform-media";
import { App } from "./App";
import "./style.css";
const root = createRoot(document.getElementById("root")!);
void start().catch((error: unknown) => {
  root.render(
    <main role="alert" className="native-startup-failure">
      <h1>MyChef</h1>
      <p>MyChef couldn’t open securely. Close the app and try again.</p>
      {error instanceof NativeStartupError && (
        <small>Reference: {error.reference}</small>
      )}
    </main>,
  );
});

async function start() {
  // Install the protected ports before any account screen can make a request.
  const native = await initializeNativePlatform();
  if (native) document.documentElement.dataset.native = "true";
  if (native)
    installNativeLinkHandling(() =>
      window.dispatchEvent(new Event("mychef-native-link-error")),
    );
  root.render(
    <React.StrictMode>
      <App />
    </React.StrictMode>,
  );
  if (!native) registerBrowserWorker();
}

function registerBrowserWorker() {
  if ("serviceWorker" in navigator) {
    const register = () => {
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
        await navigator.serviceWorker.register("/app/sw.js", {
          scope: "/app/",
        });
      })().catch(() => {
        // Shopping remains connected-only when the browser cannot install a shell.
      });
    };
    if (document.readyState === "complete") register();
    else window.addEventListener("load", register, { once: true });
  }
}
