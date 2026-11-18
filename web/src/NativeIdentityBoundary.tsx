import { useEffect, useState, type ReactNode } from "react";
import { Brand } from "./components";
import { mediaPlatform } from "./platform-media";
import {
  appBrowserFallback,
  nativeIdentityAllowed,
  nativeIdentityEvent,
  refreshNativeIdentity,
  nativeLinkFailed,
  nativeLinkFailureEvent,
  clearNativeLinkFailure,
  nativeIdentityLane,
} from "./native-app-identity";

export function NativeIdentityBoundary({ children }: { children: ReactNode }) {
  const [allowed, setAllowed] = useState(nativeIdentityAllowed);
  const [started, setStarted] = useState(allowed);
  const [checking, setChecking] = useState(false);
  const [linkFailed, setLinkFailed] = useState(nativeLinkFailed);
  useEffect(() => {
    const update = () => {
      const next = nativeIdentityAllowed();
      setAllowed(next);
      if (next) setStarted(true);
    };
    window.addEventListener(nativeIdentityEvent, update);
    update();
    return () => window.removeEventListener(nativeIdentityEvent, update);
  }, []);
  useEffect(() => {
    const update = () => setLinkFailed(nativeLinkFailed());
    window.addEventListener(nativeLinkFailureEvent, update);
    update();
    return () => window.removeEventListener(nativeLinkFailureEvent, update);
  }, []);
  if (nativeIdentityLane() !== "app") return <>{children}</>;
  return (
    <>
      {started && (
        <div hidden={!allowed} inert={!allowed}>
          {children}
        </div>
      )}
      {allowed && linkFailed && (
        <aside className="native-link-fallback" role="alert">
          <p>This link could not be opened.</p>
          <button
            onClick={() =>
              void mediaPlatform()
                ?.openExternal(appBrowserFallback)
                .catch(() => undefined)
            }
          >
            Open in browser
          </button>
          <button onClick={clearNativeLinkFailure}>Dismiss</button>
        </aside>
      )}
      {!allowed && (
        <main
          className="native-startup-failure native-identity-paused"
          role="status"
        >
          <Brand />
          <h1>This MyChef app is paused.</h1>
          <p>
            Your saved changes remain on this device. Connect to check access,
            or continue in your browser.
          </p>
          <button
            disabled={checking}
            onClick={() => {
              setChecking(true);
              void refreshNativeIdentity().finally(() => setChecking(false));
            }}
          >
            {checking ? "Checking…" : "Check again"}
          </button>
          <button
            onClick={() =>
              void mediaPlatform()
                ?.openExternal(appBrowserFallback)
                .catch(() => undefined)
            }
          >
            Open in browser
          </button>
        </main>
      )}
    </>
  );
}
