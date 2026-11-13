import { clearReminderOpen, enableReminderDevice, hasReminderBridge, peekReminderOpen, reminderDeviceActive } from "./reminder-device";
import {
  takeNativeProof,
  peekNativeProof,
  forgetNativeProof,
  nativeProofEvent,
  nativeBackEvent,
  nativeResumeEvent,
} from "./native-runtime";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { isNativeTransport, completePlatformErasure } from "./platform";
import {
  api,
  browserSignInKey,
  completeLocalLogout,
  APIError,
  getSession,
  forgetSession,
  isStale,
  sessionEpoch,
  sessionHasAccount,
  SESSION_LOST,
  message,
  type Menu,
  type MenuInput,
  type Operation,
  type Profile,
  type Recipe,
  type RecipePage,
  type Session,
} from "./api";
import { AccountLifecycle, ProfileForm, Welcome } from "./Account";
import { Feedback, RecipeRating } from "./Feedback";
import {
  EmailProofPage,
  readEmailProof,
  VerificationRequired,
} from "./EmailVerification";
import {
  Alert,
  Brand,
  Field,
  FoodPlaceholder,
  Icon,
  Startup,
} from "./components";
import { addDays, dateLabel, monday, today } from "./dates";
import { Planner } from "./Planner";
import { PasswordRecovery, readRecoveryProof } from "./PasswordRecovery";
import { Favorites, QueuedFavorites } from "./Favorites";
import { RecipeCard, RecipeDetail } from "./Recipes";
import {
  MyRecipesSection,
  PersonalCopyAction,
  PersonalRecipes,
} from "./PersonalRecipes";
import {
  cachedOwner,
  offlineOwner,
  loadShoppingCache,
  hasPendingShopping,
  localShoppingSignOut,
  signedOut,
  logoutPending,
  suspendShoppingOwner,
  resumeShoppingOwner,
  shoppingLogoutIntentKey,
} from "./shopping-cache";
import { Shopping, BackgroundShoppingSync } from "./Shopping";
import { validDate } from "./validation";

const readProofEntry = () =>
  peekNativeProof() ?? readRecoveryProof() ?? readEmailProof();

function pushAppHistory(url: string) {
  const state = isNativeTransport()
    ? { _mychefDepth: (Number(history.state?._mychefDepth) || 0) + 1 }
    : {};
  history.pushState(state, "", url);
}

type Pending = { key: string; input: MenuInput; id?: string };
export function App() {
  const [favoriteRevision, setFavoriteRevision] = useState(0);
  const [verificationRequired, setVerificationRequired] = useState(false);
  const [proofEntry, setProofEntry] = useState(readProofEntry);
  const [proofRevision, setProofRevision] = useState(0);
  useLayoutEffect(() => {
    if (peekNativeProof()) {
      forgetNativeProof();
      history.replaceState(history.state, "", "/app/" + proofEntry!.kind);
    }
    if (proofEntry && location.hash)
      history.replaceState(history.state, "", location.pathname);
  }, [proofEntry]);
  useLayoutEffect(() => {
    if (!proofEntry) return;
    const leave = () => {
      viewEpoch.current++;
      flushSync(() => setProofEntry({ kind: proofEntry.kind, proof: "" }));
    };
    window.addEventListener("pagehide", leave);
    return () => window.removeEventListener("pagehide", leave);
  }, [proofEntry?.kind]);
  const [user, setUser] = useState<Profile | null>(null),
    [booting, setBooting] = useState(true),
    [error, setError] = useState("");
  const activeAccount = useRef<Profile | null>(null);
  useEffect(() => {
    const failed = () => setError("This link could not be opened.");
    window.addEventListener("mychef-native-link-error", failed);
    return () => window.removeEventListener("mychef-native-link-error", failed);
  }, []);
  activeAccount.current = user;
  const [offlineOnly, setOfflineOnly] = useState(false),
    [deviceSigningOut, setDeviceSigningOut] = useState(false),
    [discardDialog, setDiscardDialog] = useState(false),
    [draftBackOpen, setDraftBackOpen] = useState(false);
  const [search, setSearch] = useState(
    () => new URLSearchParams(location.search),
  );
  const [recipes, setRecipes] = useState<Recipe[]>([]),
    [detail, setDetail] = useState<Recipe | null>(null),
    [loading, setLoading] = useState(false),
    [planning, setPlanning] = useState(false);
  const [operation, setOperation] = useState<Operation | null>(null),
    [submitting, setSubmitting] = useState(false),
    [pending, setPending] = useState<Pending | null>(null),
    [pollPaused, setPollPaused] = useState(false),
    [pollEpoch, setPollEpoch] = useState(0);
  const [nextOffset, setNextOffset] = useState<number | null>(null),
    [reload, setReload] = useState(0),
    [imagePolls, setImagePolls] = useState(0);
  const [planningInput, setPlanningInput] = useState<MenuInput | null>(null);
  const signOutDialog = useRef<HTMLDialogElement>(null);
  const draftBackDialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (discardDialog && !signOutDialog.current?.open)
      signOutDialog.current?.showModal();
    if (!discardDialog && signOutDialog.current?.open)
      signOutDialog.current.close();
  }, [discardDialog]);
  useEffect(() => {
    const dialog = draftBackDialog.current;
    if (draftBackOpen && dialog && !dialog.open) dialog.showModal();
  }, [draftBackOpen]);
  const swipeTransition = useRef<ViewTransition | null>(null);
  const swipeAnimation = useRef<Animation | null>(null);
  const swipeStart = useRef<{ x: number; y: number; time: number } | null>(
    null,
  );
  const resumed = useRef(""),
    sending = useRef(false);
  const viewEpoch = useRef(0),
    morePending = useRef(false);
  const personalDirty = useRef(false);
  const activeURL = useRef(location.pathname + location.search);
  activeURL.current = location.pathname + location.search;
  const requestRecovery = search.get("view") === "forgot-password";
  const recoveryView =
    proofEntry?.kind === "reset-password" || (!proofEntry && requestRecovery);
  useLayoutEffect(() => {
    if (!recoveryView) return;
    const onSignIn = (event: StorageEvent) => {
      if (event.key !== browserSignInKey || !event.newValue) return;
      viewEpoch.current++;
      forgetSession();
      flushSync(() => {
        setProofEntry(null);
        setBooting(true);
      });
      // Boot from the shared cookie in the current signed-in session.
      location.replace("/app/");
    };
    window.addEventListener("storage", onSignIn);
    return () => window.removeEventListener("storage", onSignIn);
  }, [recoveryView]);
  const week = validDate(search.get("week") ?? "")
      ? search.get("week")!
      : monday(today()),
    shoppingView = search.get("view") === "shopping",
    feedbackView = search.get("view") === "feedback",
    personalView = search.get("view") === "my-recipes",
    profileView =
      search.get("view") === "profile" || feedbackView || personalView,
    reportId = search.get("report") ?? undefined,
    key = user ? `mychef.pending.${user.id}` : "";
  function navigate(values: Record<string, string | undefined>) {
    if (
      personalDirty.current &&
      !window.confirm("Discard your unsaved recipe changes?")
    )
      return false;
    personalDirty.current = false;
    viewEpoch.current++;
    morePending.current = false;
    const params = new URLSearchParams();
    Object.entries(values).forEach(([k, v]) => {
      if (v) params.set(k, v);
    });
    const query = params.toString();
    pushAppHistory(query ? `/app/?${query}` : "/app/");
    setSearch(params);
    setProofEntry(null);
    setError("");
    setImagePolls(0);
    return true;
  }
  useEffect(() => {
    if (!user || offlineOnly || !hasReminderBridge()) return;
    let live = true;
    void api<{ deviceEnabled: boolean; current: boolean; advance: boolean }>("/planning-reminders").then((value) => {
      if (live && !reminderDeviceActive() && value.deviceEnabled && (value.current || value.advance)) return enableReminderDevice(false);
    }).catch(() => undefined);
    return () => { live = false; };
  }, [user?.accountInstance, offlineOnly]);
  useEffect(() => {
    if (!user || offlineOnly || proofEntry) return;
    let disposed = false;
    let opening = "";
    const open = () => {
      const reference = peekReminderOpen();
      if (!reference || opening === reference) return;
      opening = reference;
      void api<{ week: string }>("/planning-reminders/open", {
        method: "POST",
        body: JSON.stringify({ reference }),
      }).then(({ week: target }) => {
        if (disposed || peekReminderOpen() !== reference) return;
        clearReminderOpen(reference);
        if (validDate(target)) navigate({ week: target });
      }).catch(() => {
        if (!disposed && peekReminderOpen() === reference) {
          clearReminderOpen(reference);
          setError("This planning reminder is no longer available.");
        }
      });
    };
    window.addEventListener("mychef-reminder-open", open);
    open();
    return () => {
      disposed = true;
      window.removeEventListener("mychef-reminder-open", open);
    };
  }, [user?.accountInstance, offlineOnly, proofEntry]);
  useEffect(() => {
    if (feedbackView) window.scrollTo({ top: 0, left: 0, behavior: "instant" });
  }, [feedbackView, reportId]);
  useEffect(() => {
    if (search.get("recipe") && detail?.id === search.get("recipe"))
      window.scrollTo({ top: 0, left: 0, behavior: "instant" });
  }, [search.get("recipe"), detail?.id]);
  function swipeNavigate(next: string, direction: "left" | "right") {
    const epoch = sessionEpoch(),
      view = viewEpoch.current;
    const update = () => {
      if (epoch === sessionEpoch() && view === viewEpoch.current)
        flushSync(() =>
          navigate({ view: next === "meals" ? undefined : next, week }),
        );
    };
    swipeTransition.current?.skipTransition();
    swipeAnimation.current?.cancel();
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
      update();
      return;
    }
    document.documentElement.dataset.swipeDirection = direction;
    if (typeof document.startViewTransition === "function") {
      const transition = document.startViewTransition(update);
      swipeTransition.current = transition;
      void transition.ready.catch(() => {});
      void transition.finished
        .catch(() => {})
        .finally(() => {
          if (swipeTransition.current === transition)
            swipeTransition.current = null;
        });
    } else {
      update();
      const content = document.querySelector(".workspace");
      if (content)
        swipeAnimation.current = content.animate(
          [
            {
              transform: `translateX(${direction === "left" ? "35%" : "-35%"})`,
              opacity: 0.35,
            },
            { transform: "translateX(0)", opacity: 1 },
          ],
          { duration: 260, easing: "cubic-bezier(.2,.8,.2,1)" },
        );
    }
  }
  function clearPrivateState() {
    forgetNativeProof();
    personalDirty.current = false;
    setDraftBackOpen(false);
    swipeTransition.current?.skipTransition();
    swipeAnimation.current?.cancel();
    setRecipes([]);
    setDetail(null);
    setOperation(null);
    setPending(null);
    setPlanning(false);
    setPlanningInput(null);
    setNextOffset(null);
    resumed.current = "";
    sending.current = false;
    setSubmitting(false);
    setPollPaused(false);
    morePending.current = false;
  }
  function enterRestricted() {
    suspendShoppingOwner();
    clearPrivateState();
    setUser(null);
    setOfflineOnly(false);
    setVerificationRequired(true);
  }
  function requireSignIn() {
    forgetSession();
    suspendShoppingOwner();
    clearPrivateState();
    setUser(null);
    setVerificationRequired(false);
    setOfflineOnly(false);
  }
  function signedIn(value: Session) {
    if (signedOut()) return;
    if (value.verificationRequired) {
      enterRestricted();
      navigate({});
      return;
    }
    setVerificationRequired(false);
    const saved = cachedOwner();
    if (saved && value.user && saved.profile.id !== value.user.id) {
      suspendShoppingOwner();
      forgetSession();
      setError("Sign out of the saved account before switching accounts.");
      return;
    }
    if (value.user) resumeShoppingOwner(value.user.id);
    setOfflineOnly(false);
    clearPrivateState();
    setUser(value.user);
    navigate({});
  }
  function leaveCurrentView() {
    if (Number(history.state?._mychefDepth) > 0) history.back();
    else if (location.pathname !== "/app/" || location.search) navigate({});
  }
  useEffect(() => {
    const receive = () => {
      const entry = takeNativeProof();
      if (!entry) return;
      if (
        personalDirty.current &&
        !window.confirm("Discard your unsaved recipe changes?")
      )
        return;
      personalDirty.current = false;
      viewEpoch.current++;
      morePending.current = false;
      setProofRevision((value) => value + 1);
      setProofEntry(entry);
      // Only the non-secret route reaches history; the proof stays in memory.
      pushAppHistory("/app/" + entry.kind);
      setSearch(new URLSearchParams());
      window.scrollTo({ top: 0, left: 0, behavior: "instant" });
    };
    const back = () => {
      const dialog = [
        ...document.querySelectorAll<HTMLDialogElement>("dialog[open]"),
      ].at(-1);
      if (dialog) {
        const cancel = new Event("cancel", { cancelable: true });
        if (dialog.dispatchEvent(cancel)) dialog.close();
        return;
      }
      const field = document.activeElement;
      if (
        field instanceof HTMLInputElement ||
        field instanceof HTMLTextAreaElement ||
        field instanceof HTMLSelectElement
      ) {
        field.blur();
        return;
      }
      if (personalDirty.current) setDraftBackOpen(true);
      else leaveCurrentView();
    };
    window.addEventListener(nativeProofEvent, receive);
    window.addEventListener(nativeBackEvent, back);
    return () => {
      window.removeEventListener(nativeProofEvent, receive);
      window.removeEventListener(nativeBackEvent, back);
    };
  }, []);
  useEffect(() => {
    const pop = () => {
      if (
        personalDirty.current &&
        !window.confirm("Discard your unsaved recipe changes?")
      ) {
        pushAppHistory(activeURL.current);
        return;
      }
      personalDirty.current = false;
      viewEpoch.current++;
      morePending.current = false;
      const entry = readProofEntry();
      if (entry && location.hash)
        history.replaceState(history.state, "", location.pathname);
      setProofRevision((value) => value + 1);
      setProofEntry(entry);
      setSearch(new URLSearchParams(location.search));
    };
    window.addEventListener("popstate", pop);
    // A second link to the same proof page may only change its fragment.
    const hash = () => {
      if (location.hash && readProofEntry()) pop();
    };
    window.addEventListener("hashchange", hash);
    return () => {
      window.removeEventListener("popstate", pop);
      window.removeEventListener("hashchange", hash);
    };
  }, []);
  useEffect(() => {
    const lost = () => {
      suspendShoppingOwner();
      clearPrivateState();
      setUser(null);
      setVerificationRequired(false);
      setOfflineOnly(false);
      setBooting(false);
      setError(
        "Your session changed or expired. Sign in to the same account to recover pending shopping changes.",
      );
    };
    const sharedLogout = (event: StorageEvent) => {
      if (
        event.key !== shoppingLogoutIntentKey ||
        !event.newValue ||
        !signedOut()
      )
        return;
      forgetSession();
      sessionStorage.removeItem(key);
      clearPrivateState();
      setUser(null);
      setVerificationRequired(false);
      setOfflineOnly(false);
      setBooting(false);
      setDiscardDialog(false);
      setError("");
      navigate({});
      if (navigator.onLine) void completeLocalLogout().catch(() => undefined);
    };
    const restored = (event: PageTransitionEvent) => {
      if (!event.persisted) return;
      const previous = activeAccount.current;
      const saved = cachedOwner()?.profile;
      const changed =
        !previous ||
        !saved ||
        previous.id !== saved.id ||
        previous.accountInstance !== saved.accountInstance;
      if (signedOut() || changed) {
        const reloadAccount = !signedOut();
        flushSync(() => {
          requireSignIn();
          setBooting(reloadAccount);
          setDiscardDialog(false);
          setError("");
        });
        if (reloadAccount) location.reload();
      }
    };
    window.addEventListener("pageshow", restored);
    window.addEventListener(SESSION_LOST, lost);
    window.addEventListener("storage", sharedLogout);
    return () => {
      window.removeEventListener("pageshow", restored);
      window.removeEventListener(SESSION_LOST, lost);
      window.removeEventListener("storage", sharedLogout);
    };
  }, []);
  useEffect(() => {
    let active = true;
    async function boot() {
      if (signedOut()) {
        try {
          if (navigator.onLine) {
            await completeLocalLogout();
            if (readProofEntry() || search.get("view") === "forgot-password")
              await getSession();
          }
        } catch {
          if (active)
            setError(
              "Server sign-out is pending. Reconnect and retry before signing in.",
            );
        } finally {
          if (active) setBooting(false);
        }
        return;
      }
      try {
        const value = await getSession();
        if (!active || signedOut()) return;
        if (value.verificationRequired) {
          enterRestricted();
          return;
        }
        const saved = cachedOwner();
        if (saved && value.user?.id !== saved.profile.id) {
          suspendShoppingOwner();
          setUser(null);
          setError(
            "Sign in to the saved account to recover your shopping changes, or sign out to switch accounts.",
          );
          return;
        }
        if (value.user) resumeShoppingOwner(value.user.id);
        setUser(value.user);
      } catch (err) {
        if (!active) return;
        const saved = offlineOwner();
        if (
          err instanceof APIError &&
          err.status === 0 &&
          !readProofEntry() &&
          search.get("view") !== "forgot-password" &&
          saved &&
          loadShoppingCache(saved.profile.id, saved.week)
        ) {
          setUser(saved.profile);
          setOfflineOnly(true);
          navigate({ view: "shopping", week: saved.week });
        } else
          setError(
            err instanceof APIError && err.status === 0
              ? "Connect to load your shopping list for the first time."
              : message(err),
          );
      } finally {
        if (active) setBooting(false);
      }
    }
    void boot();
    const online = async () => {
      if (signedOut()) {
        try {
          await completeLocalLogout();
          if (active) setError("");
        } catch {
          if (active)
            setError("Server sign-out is pending. Retry before signing in.");
        }
        return;
      }
      const saved = offlineOwner();
      if (!saved) return;
      try {
        const value = await getSession();
        if (!active || signedOut()) return;
        if (value.verificationRequired) {
          enterRestricted();
          return;
        }
        if (value.user?.id !== saved.profile.id) {
          suspendShoppingOwner();
          clearPrivateState();
          setUser(null);
          setError(
            "Your saved shopping list is hidden until the same account signs in.",
          );
          return;
        }
        setOfflineOnly(false);
        setUser(value.user);
      } catch {
        /* The saved list remains available during transport failure. */
      }
    };
    window.addEventListener("online", online);
    window.addEventListener(nativeResumeEvent, online);
    return () => {
      active = false;
      window.removeEventListener("online", online);
      window.removeEventListener(nativeResumeEvent, online);
    };
  }, []);
  function reviewChangedWeek(input: MenuInput) {
    sessionStorage.removeItem(key);
    setPending(null);
    setOperation(null);
    setPlanningInput(input);
    setPlanning(true);
    setError(
      "Your week has changed. Review your menu again to choose whether to replace it.",
    );
  }
  async function submitPending(value: Pending) {
    if (!user || offlineOnly || sending.current) return;
    const started = sessionEpoch();
    sending.current = true;
    setSubmitting(true);
    setPollPaused(false);
    setError("");
    setPlanning(false);
    setPending(value);
    sessionStorage.setItem(key, JSON.stringify(value));
    try {
      const op = value.id
        ? await api<Operation>(`/generations/${encodeURIComponent(value.id)}`)
        : await api<Operation>("/generations", {
            method: "POST",
            headers: { "Idempotency-Key": value.key },
            body: JSON.stringify(value.input),
          });
      if (started !== sessionEpoch()) return;
      setOperation(op);
      setPlanningInput(null);
      const updated = { ...value, id: op.id };
      setPending(updated);
      sessionStorage.setItem(key, JSON.stringify(updated));
      setPollEpoch((v) => v + 1);
    } catch (err) {
      if (started === sessionEpoch() && !isStale(err)) {
        if (
          err instanceof APIError &&
          ["menu_exists", "favorite_conflict", "favorite_input"].includes(
            err.code,
          )
        ) {
          reviewChangedWeek(value.input);
          return;
        }
        setError(message(err));
        setPollPaused(true);
      }
    } finally {
      if (started === sessionEpoch()) {
        sending.current = false;
        setSubmitting(false);
      }
    }
  }
  useEffect(() => {
    if (!user || offlineOnly || resumed.current === user.id) return;
    resumed.current = user.id;
    try {
      const saved = sessionStorage.getItem(key);
      if (saved) {
        const value = JSON.parse(saved) as Pending;
        if (value.input && value.key) void submitPending(value);
      }
    } catch {
      sessionStorage.removeItem(key);
    }
  }, [user?.id]);
  useEffect(() => {
    if (!user || offlineOnly || !operation) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      try {
        const value = await api<Operation>(`/generations/${operation!.id}`);
        if (!active) return;
        setOperation(value);
        if (value.status === "succeeded" && value.menuId) {
          sessionStorage.removeItem(key);
          setPending(null);
          navigate({
            menu: value.menuId,
            week: pending?.input.dates[0]
              ? monday(pending.input.dates[0])
              : week,
          });
          return;
        }
        if (value.status === "failed") {
          if (value.errorCode === "menu_exists" && pending)
            reviewChangedWeek(pending.input);
          return;
        }
        timer = setTimeout(poll, 1000);
      } catch (err) {
        if (active && !isStale(err)) {
          setError(message(err));
          setPollPaused(true);
        }
      }
    }
    void poll();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [user?.id, operation?.id, pollEpoch]);
  useEffect(() => {
    if (!user || offlineOnly || profileView || shoppingView) return;
    let active = true;
    setLoading(true);
    setError("");
    const id = search.get("recipe"),
      menu = search.get("menu");
    const promise = id
      ? api<Recipe>(`/recipes/${id}`).then((value) => {
          if (active) setDetail(value);
        })
      : menu
        ? api<Menu>(`/menus/${menu}`).then((value) => {
            if (active) {
              setDetail(null);
              setRecipes(value.recipes);
              setNextOffset(null);
            }
          })
        : api<RecipePage>(`/menus?from=${week}&to=${addDays(week, 7)}`).then(
            (value) => {
              if (active) {
                setDetail(null);
                setRecipes(value.recipes);
                setNextOffset(value.nextOffset);
              }
            },
          );
    promise
      .catch((err) => {
        if (active && !isStale(err)) {
          setError(message(err));
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [user?.id, search.toString(), reload]);
  useEffect(() => {
    if (
      !user ||
      offlineOnly ||
      shoppingView ||
      profileView ||
      imagePolls >= 120 ||
      !(
        detail?.imageStatus === "pending" ||
        (!detail && recipes.some((r) => r.imageStatus === "pending"))
      )
    )
      return;
    const timer = setTimeout(() => {
      setImagePolls((v) => v + 1);
      setReload((v) => v + 1);
    }, 2500);
    return () => clearTimeout(timer);
  }, [recipes, detail, imagePolls, profileView]);
  function deletedAccount() {
    sessionStorage.removeItem(key);
    // Clear the document before navigation so its history entry cannot retain
    // the account view for a later back/forward restoration.
    flushSync(() => {
      clearPrivateState();
      setUser(null);
      setVerificationRequired(false);
      setOfflineOnly(false);
      setDiscardDialog(false);
      setError("");
    });
    location.replace(isNativeTransport() ? "/app/" : "/landing/");
  }
  async function logout(discard = false) {
    const owner = user?.id ?? cachedOwner()?.profile.id;
    if (
      !verificationRequired &&
      owner &&
      hasPendingShopping(owner) &&
      !discard
    ) {
      setDiscardDialog(true);
      return;
    }
    localShoppingSignOut(verificationRequired);
    forgetSession();
    if (isNativeTransport()) {
      setDeviceSigningOut(true);
      try {
        await completePlatformErasure();
      } catch {
        setError("Device sign-out did not finish. Tap Sign out to retry.");
        return;
      } finally {
        setDeviceSigningOut(false);
      }
    }
    sessionStorage.removeItem(key);
    clearPrivateState();
    setUser(null);
    setVerificationRequired(false);
    setOfflineOnly(false);
    setDiscardDialog(false);
    navigate({});
    try {
      if (navigator.onLine) await completeLocalLogout();
    } catch {
      setError(
        "You are signed out on this device. Server sign-out will finish when you reconnect; retry before signing in.",
      );
    }
  }
  const discardPrompt = discardDialog && (
    <dialog
      ref={signOutDialog}
      className="replacement-dialog"
      aria-labelledby="shopping-discard-title"
      onCancel={() => setDiscardDialog(false)}
    >
      <h2 id="shopping-discard-title">Unsaved shopping changes</h2>
      <p>
        Some shopping edits have not reached the server. Stay to keep them, or
        discard them and sign out.
      </p>
      <button
        className="button primary"
        autoFocus
        onClick={() => setDiscardDialog(false)}
      >
        Stay
      </button>{" "}
      <button className="button" onClick={() => void logout(true)}>
        Discard changes and sign out
      </button>
    </dialog>
  );
  async function more() {
    if (nextOffset == null || morePending.current) return;
    const started = sessionEpoch(),
      view = viewEpoch.current;
    morePending.current = true;
    try {
      const value = await api<RecipePage>(
        `/menus?from=${week}&to=${addDays(week, 7)}&offset=${nextOffset}`,
      );
      if (started !== sessionEpoch() || view !== viewEpoch.current) return;
      setRecipes((v) => [...v, ...value.recipes]);
      setNextOffset(value.nextOffset);
    } catch (err) {
      if (
        started === sessionEpoch() &&
        view === viewEpoch.current &&
        !isStale(err)
      )
        setError(message(err));
    } finally {
      if (started === sessionEpoch() && view === viewEpoch.current)
        morePending.current = false;
    }
  }
  if (deviceSigningOut) return <Startup message="Signing out…" />;
  if (booting) return <Startup />;
  if (recoveryView)
    return (
      <>
        {discardPrompt}
        {error && <Alert>{error}</Alert>}
        <PasswordRecovery
          key={`${proofEntry ? proofRevision : "request"}:${sessionEpoch()}`}
          entry={proofEntry?.kind === "reset-password" ? proofEntry : undefined}
          signedIn={sessionHasAccount()}
          viewGeneration={() => viewEpoch.current}
          onRequest={() => navigate({ view: "forgot-password" })}
          onSignOut={() => void logout()}
          onExit={() => navigate({})}
          onForgetProof={() =>
            setProofEntry({ kind: "reset-password", proof: "" })
          }
        />
      </>
    );
  if (proofEntry)
    return (
      <>
        {discardPrompt}
        {error && <Alert>{error}</Alert>}
        <EmailProofPage
          key={proofRevision}
          entry={proofEntry}
          restricted={verificationRequired}
          signedIn={!!user}
          onVerified={signedIn}
          onReauthenticate={requireSignIn}
          onSignOut={() => void logout()}
          onExit={() => navigate({})}
        />
      </>
    );
  if (verificationRequired)
    return (
      <>
        <VerificationRequired onSignOut={() => void logout()} />
        <section className="panel restricted-account-data">
          <AccountLifecycle active onDeleted={deletedAccount} />
        </section>
      </>
    );
  if (!user)
    return (
      <>
        {error && <Alert>{error}</Alert>}
        {cachedOwner() && (
          <button className="button" onClick={() => void logout()}>
            Sign out of saved account
          </button>
        )}
        {logoutPending() && (
          <button
            className="button"
            onClick={() =>
              void completeLocalLogout()
                .then(() => setError(""))
                .catch(() =>
                  setError("Server sign-out failed. Reconnect and retry."),
                )
            }
          >
            Retry server sign-out
          </button>
        )}
        {discardPrompt}
        <Welcome
          onSession={signedIn}
          onForgotPassword={() => navigate({ view: "forgot-password" })}
        />
      </>
    );
  const working =
    submitting ||
    (!!operation &&
      ["queued", "running"].includes(operation.status) &&
      !pollPaused);
  return (
    <>
      {discardPrompt}
      {draftBackOpen && (
        <dialog
          ref={draftBackDialog}
          className="replacement-dialog"
          aria-labelledby="draft-back-title"
          onCancel={() => setDraftBackOpen(false)}
        >
          <h2 id="draft-back-title">Discard unsaved recipe changes?</h2>
          <button
            className="button primary"
            autoFocus
            onClick={() => setDraftBackOpen(false)}
          >
            Keep editing
          </button>{" "}
          <button
            className="button"
            onClick={() => {
              personalDirty.current = false;
              setDraftBackOpen(false);
              leaveCurrentView();
            }}
          >
            Discard changes
          </button>
        </dialog>
      )}
      <header className="app-header">
        <Brand
          disabled={offlineOnly}
          onHome={() => {
            if (navigate({ week }))
              window.scrollTo({ top: 0, left: 0, behavior: "instant" });
          }}
        />
      </header>
      <div
        className="app-shell"
        onTouchStart={(event) => {
          swipeStart.current = null;
          if (
            event.touches.length !== 1 ||
            document.querySelector("dialog[open]") ||
            (event.target as Element).closest(
              "button,input,select,textarea,label,a,summary,form,[contenteditable]",
            )
          )
            return;
          const point = event.touches[0];
          if (point.clientX < 24 || point.clientX > window.innerWidth - 24)
            return;
          swipeStart.current = {
            x: point.clientX,
            y: point.clientY,
            time: event.timeStamp,
          };
        }}
        onTouchCancel={() => {
          swipeStart.current = null;
        }}
        onTouchEnd={(event) => {
          const start = swipeStart.current;
          swipeStart.current = null;
          if (
            !start ||
            event.changedTouches.length !== 1 ||
            document.querySelector("dialog[open]")
          )
            return;
          const point = event.changedTouches[0],
            dx = point.clientX - start.x,
            dy = point.clientY - start.y;
          if (
            Math.abs(dx) < 64 ||
            Math.abs(dx) < Math.abs(dy) * 1.5 ||
            event.timeStamp - start.time > 800
          )
            return;
          const pages = offlineOnly
            ? ["shopping", "profile"]
            : ["meals", "shopping", "profile"];
          const current = shoppingView
              ? "shopping"
              : profileView
                ? "profile"
                : "meals",
            index = pages.indexOf(current),
            next = pages[index + (dx < 0 ? 1 : -1)];
          if (next) swipeNavigate(next, dx < 0 ? "left" : "right");
        }}
      >
        <aside className="sidebar">
          <nav aria-label="Main navigation">
            <button
              className={!profileView && !shoppingView ? "active" : ""}
              aria-current={!profileView && !shoppingView ? "page" : undefined}
              disabled={offlineOnly}
              onClick={() => navigate({ week })}
            >
              <Icon name="meal" />
              Meals
            </button>
            <button
              className={shoppingView ? "active" : ""}
              aria-current={shoppingView ? "page" : undefined}
              onClick={() => navigate({ view: "shopping", week })}
            >
              <Icon name="basket" />
              Shopping list
            </button>
            <button
              className={profileView ? "active" : ""}
              aria-current={profileView ? "page" : undefined}
              onClick={() => navigate({ view: "profile", week })}
            >
              <Icon name="person" />
              Profile
            </button>
          </nav>
        </aside>
        <main className="workspace">
          {!shoppingView &&
            cachedOwner()?.profile.id === user.id &&
            hasPendingShopping(user.id) && (
              <BackgroundShoppingSync
                key={`${user.id}/${user.accountInstance}`}
                profile={user}
                week={cachedOwner()!.week}
                onOpen={(next) => navigate({ view: "shopping", week: next })}
              />
            )}
          {error && <Alert>{error}</Alert>}
          {!offlineOnly &&
            (profileView || search.get("return") === "profile") && (
              <div hidden={!profileView || feedbackView || personalView}>
                <ProfileForm
                  key={user.id}
                  active={profileView && !feedbackView && !personalView}
                  user={user}
                  onSave={setUser}
                  onDeleted={deletedAccount}
                  onLogout={() => void logout()}
                  onFeedback={() => navigate({ view: "feedback", week })}
                  personalRecipes={
                    <MyRecipesSection
                      onOpen={() => navigate({ view: "my-recipes", week })}
                    />
                  }
                  favorites={
                    <Favorites
                      key={`${user.id}/${favoriteRevision}`}
                      onOpen={(r) =>
                        navigate({ recipe: r.id, return: "profile", week })
                      }
                      onWeek={(next) => navigate({ week: next })}
                      onCopy={(recipe) =>
                        navigate({ view: "my-recipes", copy: recipe.id, week })
                      }
                    />
                  }
                />
              </div>
            )}
          {shoppingView ? (
            <Shopping
              key={`${user.id}/${user.accountInstance}/${week}`}
              profile={user}
              offlineOnly={offlineOnly}
              week={week}
              onWeek={(next) => navigate({ view: "shopping", week: next })}
            />
          ) : personalView && !offlineOnly ? (
            <PersonalRecipes
              key={`${user.id}/${sessionEpoch()}/${viewEpoch.current}`}
              copySource={search.get("copy") ?? undefined}
              onCopyResolved={() => {
                const params = new URLSearchParams(search);
                params.delete("copy");
                history.replaceState(history.state, "", `/app/?${params}`);
                setSearch(params);
              }}
              onDirty={(dirty) => {
                personalDirty.current = dirty;
              }}
              onBack={() => navigate({ view: "profile", week })}
              onPlanned={() => setFavoriteRevision((value) => value + 1)}
            />
          ) : feedbackView && !offlineOnly ? (
            <Feedback
              key={`${user.id}/${sessionEpoch()}/${viewEpoch.current}`}
              owner={user.id}
              generation={viewEpoch.current}
              viewGeneration={() => viewEpoch.current}
              recipeId={reportId}
              onBack={() =>
                navigate(
                  reportId
                    ? {
                        recipe: reportId,
                        week,
                        menu: search.get("menu") ?? undefined,
                        return: search.get("return") ?? undefined,
                      }
                    : { view: "profile", week },
                )
              }
            />
          ) : profileView && offlineOnly ? (
            <section className="panel">
              <h2>Profile</h2>
              <p>You’re offline. Connect to manage your profile.</p>
              <button className="button" onClick={() => void logout()}>
                Sign out
              </button>
            </section>
          ) : profileView ? null : detail &&
            detail.id === search.get("recipe") ? (
            <RecipeDetail
              recipe={detail}
              copyAction={
                <PersonalCopyAction
                  key={detail.id}
                  onCopy={() =>
                    navigate({ view: "my-recipes", copy: detail.id, week })
                  }
                />
              }
              onReport={() =>
                navigate({
                  view: "feedback",
                  report: detail.id,
                  week,
                  menu: search.get("menu") ?? undefined,
                  return: search.get("return") ?? undefined,
                })
              }
              rating={
                <RecipeRating
                  key={`${user.id}/${sessionEpoch()}/${viewEpoch.current}/${detail.id}`}
                  owner={user.id}
                  generation={viewEpoch.current}
                  viewGeneration={() => viewEpoch.current}
                  recipeId={detail.id}
                />
              }
              onFavorite={(r) => {
                setDetail(r);
                setFavoriteRevision((v) => v + 1);
              }}
              fromProfile={search.get("return") === "profile"}
              onBack={() =>
                navigate(
                  search.get("return") === "profile"
                    ? { view: "profile", week }
                    : { menu: search.get("menu") ?? undefined, week },
                )
              }
            />
          ) : (
            <>
              <section className="hero">
                <div>
                  <span className="eyebrow">Weekly menu</span>
                  <h2>
                    Hello, {user.firstName}.<br />
                    Let’s plan your week.
                  </h2>
                  <p>A few choices. Meals that fit.</p>
                  <button
                    className="button primary"
                    disabled={working || !!pending}
                    onClick={() => {
                      setPlanning(true);
                      setError("");
                    }}
                  >
                    <Icon name="plus" />
                    Plan a menu
                  </button>
                </div>
                <div className="hero-art">
                  <FoodPlaceholder />
                  <span>
                    Made around you.
                    <br />
                    <strong>Ready for your table.</strong>
                  </span>
                </div>
              </section>
              {working && (
                <div className="progress panel" role="status">
                  <span className="spinner" />
                  <div>
                    <strong>
                      {submitting
                        ? "Checking your request…"
                        : operation?.stage === "saving"
                          ? "Saving your meals…"
                          : operation?.status === "queued"
                            ? "Your menu is in the queue…"
                            : "MyChef is preparing your menu…"}
                    </strong>
                    <p>
                      You can refresh this page. We’ll keep track of your menu
                      while it’s being prepared.
                    </p>
                  </div>
                </div>
              )}
              {operation?.status === "failed" && (
                <Alert>
                  <strong>Your menu could not be completed.</strong>
                  <p>
                    {operation.errorMessage ??
                      "Review the request before trying again."}
                  </p>
                  <button
                    className="button secondary"
                    onClick={() => {
                      sessionStorage.removeItem(key);
                      setPending(null);
                      setOperation(null);
                      setPlanning(true);
                    }}
                  >
                    Plan a new request
                  </button>
                </Alert>
              )}
              {pending && !working && operation?.status !== "failed" && (
                <div className="notice">
                  <p>
                    Your last request can be checked without creating a new one.
                  </p>
                  <button
                    className="button secondary"
                    onClick={() => void submitPending(pending)}
                  >
                    Check request status
                  </button>
                </div>
              )}
              {planning && (
                <Planner
                  preferences={user.preferences}
                  initialInput={planningInput}
                  onCancel={() => setPlanning(false)}
                  onSubmit={(input) =>
                    void submitPending({ key: crypto.randomUUID(), input })
                  }
                />
              )}
              <section className="menu-section">
                <div className="section-title">
                  <div>
                    {search.get("menu") && recipes.length > 0 && !loading ? (
                      <span className="menu-complete" role="status">
                        <span aria-hidden="true">✓</span> Menu ready
                      </span>
                    ) : (
                      <span className="eyebrow">Your table</span>
                    )}
                    <h3>
                      {search.get("menu")
                        ? "Your new menu"
                        : `Week of ${dateLabel(week)}`}
                    </h3>
                  </div>
                  <div className="week-picker">
                    <button
                      aria-label="Previous week"
                      onClick={() => navigate({ week: addDays(week, -7) })}
                    >
                      ←
                    </button>
                    <Field label="Browse week">
                      <input
                        type="date"
                        value={week}
                        onChange={(e) => {
                          if (validDate(e.target.value))
                            navigate({ week: monday(e.target.value) });
                        }}
                      />
                    </Field>
                    <button
                      aria-label="Next week"
                      onClick={() => navigate({ week: addDays(week, 7) })}
                    >
                      →
                    </button>
                  </div>
                </div>
                <QueuedFavorites
                  key={`${user.id}/${week}/${search.get("menu") ?? ""}`}
                  week={week}
                />
                {loading && !recipes.length ? (
                  <p role="status" className="muted">
                    Loading your recipes…
                  </p>
                ) : recipes.length ? (
                  <>
                    <div className="recipe-grid">
                      {recipes.map((recipe) => (
                        <RecipeCard
                          key={recipe.id}
                          recipe={recipe}
                          onOpen={() =>
                            navigate({
                              recipe: recipe.id,
                              menu: search.get("menu") ?? undefined,
                              week,
                            })
                          }
                        />
                      ))}
                    </div>
                    {nextOffset != null && (
                      <button
                        className="button secondary load-more"
                        onClick={more}
                      >
                        Load more recipes
                      </button>
                    )}
                  </>
                ) : (
                  <div className="empty-state">
                    <Icon />
                    <h4>No menu saved for this week.</h4>
                    <button
                      className="text-button"
                      onClick={() => setPlanning(true)}
                    >
                      Plan a menu →
                    </button>
                  </div>
                )}
                {recipes.some((r) => r.imageStatus === "unavailable") && (
                  <p className="notice" role="status">
                    Some recipe images are unavailable. Your meals and
                    instructions are saved.
                  </p>
                )}
                {recipes.some((r) => r.imageStatus === "pending") && (
                  <p className="muted small">
                    Your recipes are saved. Images are being prepared
                    separately.
                  </p>
                )}
              </section>
            </>
          )}
          <footer className="app-footer">
            
          </footer>
        </main>
      </div>
    </>
  );
}
