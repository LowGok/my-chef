import { nativeAppActive, nativeResumeEvent } from "./native-runtime";
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
  flushShoppingCache,
  ShoppingStorageError,
  shoppingCacheEpoch,
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

const reviewMessage =
  "This list or its meals changed. Review the latest list and retry your pending changes.";
const needsReview = (error: unknown) =>
  error instanceof ShoppingConflict ||
  (error instanceof APIError && error.status === 409);

export function useShopping(
  profile: Profile,
  week: string,
  offlineOnly: boolean,
) {
  const owner = profile.id;
  const incarnation = profile.accountInstance;
  const cacheEpoch = useRef(shoppingCacheEpoch()).current;
  const initial = () => loadShoppingCache(owner, week);
  const [packet, setPacket] = useState<ShoppingCache | null>(initial),
    [error, setError] = useState(packet?.reviewRequired ? reviewMessage : ""),
    [busy, setBusy] = useState(false),
    [savingOnDevice, setSavingOnDevice] = useState(false),
    [offline, setOffline] = useState(offlineOnly || !navigator.onLine),
    [conflict, setConflict] = useState(!!packet?.reviewRequired),
    [notice, setNotice] = useState(""),
    [blockedWeek, setBlockedWeek] = useState<string | null>(
      packet?.reviewRequired ? week : null,
    );
  const current = useRef(packet),
    reviewWeek = useRef(blockedWeek),
    active = useRef(true),
    syncing = useRef(false),
    localWrites = useRef(0);
  async function publish(value: ShoppingCache, targetWeek = week) {
    if (
      !active.current ||
      shoppingCacheEpoch() !== cacheEpoch ||
      signedOut() ||
      cachedOwner()?.profile.id !== owner ||
      cachedOwner()?.profile.accountInstance !== incarnation
    )
      return;
    saveShoppingCache(owner, targetWeek, value, incarnation, cacheEpoch);
    if (targetWeek === week) {
      const remembered = loadShoppingCache(owner, targetWeek) ?? value;
      current.current = remembered;
      setPacket(remembered);
    }
    await flushShoppingCache();
  }
  async function sync(force = false) {
    if (
      !nativeAppActive() ||
      syncing.current ||
      (reviewWeek.current !== null && (!force || reviewWeek.current !== week)) ||
      !active.current ||
      signedOut() ||
      shoppingCacheEpoch() !== cacheEpoch
    )
      return;
    if (!navigator.onLine) {
      setOffline(true);
      return;
    }
    syncing.current = true;
    setBusy(true);
    let syncingWeek = week;
    let repeat = false;
    try {
      await flushShoppingCache();
      const session = await getSession();
      if (!active.current || signedOut() || shoppingCacheEpoch() !== cacheEpoch)
        return;
      if (
        session.user?.id !== owner ||
        session.user.accountInstance !== incarnation
      ) {
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
        if (
          !active.current ||
          signedOut() ||
          shoppingCacheEpoch() !== cacheEpoch
        )
          return;
        rememberShoppingOwner(profile, week, cacheEpoch);
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
          const reviewed = force && targetWeek === week;
          if (saved?.reviewRequired && !reviewed) {
            reviewWeek.current = targetWeek;
            throw new ShoppingConflict(reviewMessage);
          }
          let result: ShoppingList;
          try {
            const desired = replayShopping(latest, queue, reviewed);
            result = await api<ShoppingList>(`/shopping/${targetWeek}`, {
              method: "PUT",
              body: JSON.stringify(desired),
            });
          } catch (err) {
            if (needsReview(err)) {
              reviewWeek.current = targetWeek;
              const base =
                err instanceof APIError
                  ? await api<ShoppingList>(`/shopping/${targetWeek}`).catch(
                      () => latest,
                    )
                  : latest;
              const pending =
                loadShoppingCache(owner, targetWeek)?.queue ?? queue;
              await publish(
                { base, queue: pending, reviewRequired: true },
                targetWeek,
              );
            }
            throw err;
          }
          if (
            !active.current ||
            signedOut() ||
            shoppingCacheEpoch() !== cacheEpoch
          )
            return;
          const sent = new Set(queue.map((op) => op.key));
          const remaining = (
            loadShoppingCache(owner, targetWeek)?.queue ?? queue
          ).filter((op) => !sent.has(op.key));
          await publish({ base: result, queue: remaining }, targetWeek);
          if (remaining.length) repeat = true;
        } else await publish({ base: latest, queue: [] }, targetWeek);
        if (reviewWeek.current === targetWeek) reviewWeek.current = null;
      }
      setBlockedWeek(null);
      setConflict(false);
      setError("");
    } catch (err) {
      if (
        !active.current ||
        isStale(err) ||
        shoppingCacheEpoch() !== cacheEpoch
      )
        return;
      if (err instanceof APIError && err.status === 0) {
        setOffline(true);
        setError(
          "Connection unavailable. Shopping edits stay on this device until they can sync.",
        );
      } else {
        setBlockedWeek(syncingWeek);
        setConflict(reviewWeek.current === syncingWeek || needsReview(err));
        setError(
          err instanceof ShoppingConflict || err instanceof ShoppingStorageError
            ? err.message
            : message(err),
        );
      }
    } finally {
      syncing.current = false;
      if (active.current) {
        setBusy(false);
        if (repeat && !reviewWeek.current) queueMicrotask(() => void sync());
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
    window.addEventListener(nativeResumeEvent, online);
    window.addEventListener("offline", off);
    return () => {
      active.current = false;
      clearInterval(timer);
      window.removeEventListener("online", online);
      window.removeEventListener(nativeResumeEvent, online);
      window.removeEventListener("offline", off);
    };
  }, [owner, incarnation, week, offlineOnly, conflict]);
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
  async function edit(edit: ShoppingEdit) {
    const saved = loadShoppingCache(owner, week) ?? current.current;
    if (!saved) return;
    let visible = saved.base;
    for (const op of saved.queue) visible = editShopping(visible, op.edit);
    const op = queueShoppingEdit(visible, edit);
    localWrites.current++;
    setSavingOnDevice(true);
    try {
      await publish({ ...saved, queue: [...saved.queue, op] });
      if (!active.current || shoppingCacheEpoch() !== cacheEpoch) return;
      if (!reviewWeek.current) {
        setError("");
        void sync();
      }
    } catch (error) {
      setError(
        error instanceof ShoppingStorageError
          ? `${error.message} Keep the app open and retry.`
          : "This browser could not save the edit locally. Free some browser storage and retry.",
      );
    } finally {
      localWrites.current--;
      if (active.current && localWrites.current === 0) setSavingOnDevice(false);
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
    savingOnDevice,
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
