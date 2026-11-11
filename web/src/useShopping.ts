import { useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  getSession,
  isStale,
  message,
  SESSION_LOST,
  type Profile,
} from "./api";
import {
  cachedOwner,
  loadShoppingCache,
  pendingShoppingWeeks,
  rememberShoppingOwner,
  saveShoppingCache,
  signedOut,
  suspendShoppingOwner,
  type ShoppingCache,
} from "./shopping-cache";
import {
  editShopping,
  queueShoppingEdit,
  replayShopping,
  ShoppingConflict,
  type ShoppingEdit,
  type ShoppingList,
} from "./shopping-state";

export function useShopping(
  profile: Profile,
  week: string,
  offlineOnly: boolean,
) {
  const owner = profile.id;
  const initial = () => loadShoppingCache(owner, week);
  const [packet, setPacket] = useState<ShoppingCache | null>(initial),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [offline, setOffline] = useState(offlineOnly || !navigator.onLine),
    [conflict, setConflict] = useState(false),
    [notice, setNotice] = useState(""),
    [blockedWeek, setBlockedWeek] = useState<string | null>(null);
  const current = useRef(packet),
    active = useRef(true),
    syncing = useRef(false);
  function publish(value: ShoppingCache, targetWeek = week) {
    if (!active.current || signedOut() || cachedOwner()?.profile.id !== owner)
      return;
    saveShoppingCache(owner, targetWeek, value);
    if (targetWeek === week) {
      const remembered = loadShoppingCache(owner, targetWeek) ?? value;
      current.current = remembered;
      setPacket(remembered);
    }
  }
  async function sync(force = false) {
    if (syncing.current || !active.current || signedOut()) return;
    if (!navigator.onLine) {
      setOffline(true);
      return;
    }
    syncing.current = true;
    setBusy(true);
    let syncingWeek = week;
    let repeat = false;
    try {
      const session = await getSession();
      if (!active.current || signedOut()) return;
      if (session.user?.id !== owner) {
        suspendShoppingOwner();
        window.dispatchEvent(new Event(SESSION_LOST));
        return;
      }
      setOffline(false);
      const weeks = [
        week,
        ...pendingShoppingWeeks(owner).filter(
          (candidate) => candidate !== week,
        ),
      ];
      for (const targetWeek of weeks) {
        syncingWeek = targetWeek;
        const latest = await api<ShoppingList>(`/shopping/${targetWeek}`);
        if (!active.current || signedOut()) return;
        rememberShoppingOwner(profile, week);
        const saved =
          loadShoppingCache(owner, targetWeek) ??
          (targetWeek === week ? current.current : null);
        if (
          targetWeek === week &&
          saved &&
          saved.base.sourceVersion !== latest.sourceVersion
        )
          setNotice(
            "Your meals changed. The list is up to date; changed amounts are back in To buy.",
          );
        const queue = saved?.queue ?? [];
        if (queue.length) {
          let desired: ShoppingList;
          try {
            desired = replayShopping(
              latest,
              queue,
              force && targetWeek === week,
            );
          } catch (err) {
            publish({ base: latest, queue }, targetWeek);
            throw err;
          }
          const result = await api<ShoppingList>(`/shopping/${targetWeek}`, {
            method: "PUT",
            body: JSON.stringify(desired),
          });
          if (!active.current || signedOut()) return;
          const sent = new Set(queue.map((op) => op.key));
          const remaining = (
            loadShoppingCache(owner, targetWeek)?.queue ?? queue
          ).filter((op) => !sent.has(op.key));
          publish({ base: result, queue: remaining }, targetWeek);
          if (remaining.length) repeat = true;
        } else publish({ base: latest, queue: [] }, targetWeek);
      }
      setBlockedWeek(null);
      setConflict(false);
      setError("");
    } catch (err) {
      if (!active.current || isStale(err)) return;
      if (err instanceof APIError && err.status === 0) {
        setOffline(true);
        setError(
          "Connection unavailable. Shopping edits stay on this device until they can sync.",
        );
      } else {
        setBlockedWeek(syncingWeek);
        setConflict(
          err instanceof ShoppingConflict ||
            (err instanceof APIError && err.status === 409),
        );
        setError(err instanceof ShoppingConflict ? err.message : message(err));
      }
    } finally {
      syncing.current = false;
      if (active.current) {
        setBusy(false);
        if (repeat) queueMicrotask(() => void sync());
      }
    }
  }
  useEffect(() => {
    active.current = true;
    if (!offlineOnly) void sync();
    const online = () => {
        setOffline(false);
        void sync();
      },
      off = () => setOffline(true);
    const timer = setInterval(() => {
      if (!conflict && !offlineOnly) void sync();
    }, 5000);
    window.addEventListener("online", online);
    window.addEventListener("offline", off);
    return () => {
      active.current = false;
      clearInterval(timer);
      window.removeEventListener("online", online);
      window.removeEventListener("offline", off);
    };
  }, [owner, week, offlineOnly, conflict]);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => {
      const value = current.current;
      if (
        (value?.privateImports || value?.base.privateImports) &&
        value.queue.length
      ) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, []);
  function edit(edit: ShoppingEdit) {
    const saved = loadShoppingCache(owner, week) ?? current.current;
    if (!saved) return;
    let visible = saved.base;
    for (const op of saved.queue) visible = editShopping(visible, op.edit);
    const op = queueShoppingEdit(visible, edit);
    try {
      publish({ ...saved, queue: [...saved.queue, op] });
      setError("");
      if (!conflict) void sync();
    } catch {
      setError(
        "This browser could not save the edit locally. Free some browser storage and retry.",
      );
    }
  }
  let list = packet?.base ?? null;
  if (list && !(conflict && blockedWeek === week))
    for (const op of packet?.queue ?? []) list = editShopping(list, op.edit);
  return {
    list,
    tabOnly: !!(packet?.privateImports || packet?.base.privateImports),
    error,
    busy,
    offline,
    conflict,
    notice,
    blockedWeek,
    recoveryKeepsRemoved: !!packet?.queue.some(
      (op) =>
        op.edit.kind === "state" &&
        !packet.base.items.some(
          (item) =>
            item.id === op.edit.id || item.id === `recovered-${op.edit.id}`,
        ),
    ),
    pending: pendingShoppingWeeks(owner).reduce(
      (count, pendingWeek) =>
        count + (loadShoppingCache(owner, pendingWeek)?.queue.length ?? 0),
      0,
    ),
    edit,
    retry: () => void sync(conflict),
  };
}
