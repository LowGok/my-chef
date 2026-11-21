import { analyticsRevision, queuedAnalyticsRevision } from "./analytics-state";
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
  stageFreeShopping,
  type ShoppingBlockedChange,
  type ShoppingEdit,
  type ShoppingList,
} from "./shopping-state";

const reviewMessage =
  "This list or its meals changed. Review the latest list and retry your pending changes.";
const blockedLabels: Record<ShoppingBlockedChange, string> = {
  meals: "meal selection",
  extras: "edits to extras",
  order: "item order",
  unavailable: "check-offs for items no longer on the list",
};

function blockedShoppingNotice(changes: ShoppingBlockedChange[] | undefined) {
  const labels = Object.entries(blockedLabels)
    .filter(([key]) => changes?.includes(key as ShoppingBlockedChange))
    .map(([, label]) => label);
  return labels.length
    ? `Some shopping changes weren’t synced on Free: ${labels.join(", ")}.`
    : "";
}

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
      (reviewWeek.current !== null &&
        (!force || reviewWeek.current !== week)) ||
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
        let latest: ShoppingList;
        try {
          latest = await api<ShoppingList>(`/shopping/${targetWeek}`);
        } catch (err) {
          // Stop showing a cached week after an authoritative denial.
          // Its edits stay on the device and do not block other weeks.
          if (err instanceof APIError && err.status === 404) {
            if (
              targetWeek === week &&
              active.current &&
              shoppingCacheEpoch() === cacheEpoch
            ) {
              current.current = null;
              setPacket(null);
            }
            setNotice(
              "Some weeks are unavailable. Unsynced edits stay on this device until that week is available.",
            );
            continue;
          }
          throw err;
        }
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
          saved.base.customizationLocked === latest.customizationLocked &&
          saved.base.sourceVersion !== latest.sourceVersion
        )
          setNotice(
            "Your meals changed. The list is up to date; changed amounts are back in To buy.",
          );
        const queue = saved?.queue ?? [];
        if (queue.length) {
          const reviewed = force && targetWeek === week;
          const staged = latest.customizationLocked
            ? stageFreeShopping(latest, queue, reviewed)
            : undefined;
          if (!staged && saved?.reviewRequired && !reviewed) {
            reviewWeek.current = targetWeek;
            throw new ShoppingConflict(reviewMessage);
          }
          let result: ShoppingList;
          try {
            const desired =
              staged?.desired ?? replayShopping(latest, queue, reviewed);
            result =
              staged?.applied === 0
                ? latest
                : await api<ShoppingList>(`/shopping/${targetWeek}`, {
                    method: "PUT",
                    body: JSON.stringify(desired),
                    headers: {
                      "X-MyChef-Analytics-Revision": String(queuedAnalyticsRevision(queue)),
                    },
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
                {
                  base,
                  queue: pending,
                  reviewRequired: true,
                  blockedChanges: saved?.blockedChanges,
                },
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
          const keepForReview = new Set(staged?.review.map((op) => op.key));
          const sent = new Set(
            queue
              .filter((op) => !keepForReview.has(op.key))
              .map((op) => op.key),
          );
          const remaining = (
            loadShoppingCache(owner, targetWeek)?.queue ?? queue
          ).filter((op) => !sent.has(op.key));
          const blockedChanges = [
            ...new Set([
              ...(saved?.blockedChanges ?? []),
              ...(staged?.blocked ?? []),
              ...(result.blockedChanges ?? []),
            ]),
          ];
          const requiresReview = !!staged?.review.length;
          await publish(
            {
              base: result,
              queue: remaining,
              reviewRequired: requiresReview ? true : undefined,
              blockedChanges,
            },
            targetWeek,
          );
          if (requiresReview) {
            reviewWeek.current = targetWeek;
            throw new ShoppingConflict(
              "Some item amounts or check-offs changed. Review the latest list and retry the remaining check-offs.",
            );
          }
          if (remaining.length) repeat = true;
        } else {
          await publish(
            { base: latest, queue: [], blockedChanges: saved?.blockedChanges },
            targetWeek,
          );
        }
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
    if (!saved || (saved.base.customizationLocked && edit.kind !== "state"))
      return;
    let visible = saved.base;
    for (const op of saved.queue) visible = editShopping(visible, op.edit);
    const op = {
      ...queueShoppingEdit(visible, edit),
      analyticsRevision: analyticsRevision(),
    };
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
    downgradeNotice: blockedShoppingNotice(packet?.blockedChanges),
    dismissDowngradeNotice: () => {
      const saved = loadShoppingCache(owner, week) ?? current.current;
      if (saved)
        void publish({ ...saved, blockedChanges: [] }).catch(() => undefined);
    },
    blockedWeek,
    recoveryKeepsRemoved:
      !list?.customizationLocked &&
      !!packet?.queue.some(
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
