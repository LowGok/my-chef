import type { components } from "./api.generated";
export type ShoppingList = components["schemas"]["ShoppingList"];
export type ShoppingItem = components["schemas"]["ShoppingItem"];
export type ShoppingState = ShoppingItem["state"];
export type ShoppingEdit =
  | { kind: "state"; id: string; state: ShoppingState }
  | { kind: "meal"; id: string; selected: boolean }
  | {
      kind: "extra";
      id: string;
      name: string;
      quantity: string | null;
      unit: string;
    }
  | { kind: "remove"; id: string };
export function editShopping(
  list: ShoppingList,
  edit: ShoppingEdit,
): ShoppingList {
  const next = structuredClone(list);
  switch (edit.kind) {
    case "state":
      next.items = next.items.map((i) =>
        i.id === edit.id ? { ...i, state: edit.state, updated: false } : i,
      );
      break;
    case "meal":
      next.meals = next.meals.map((m) =>
        m.position === edit.id ? { ...m, selected: edit.selected } : m,
      );
      break;
    case "remove":
      next.items = next.items.filter((i) => !i.manual || i.id !== edit.id);
      break;
    case "extra":
      if (!next.items.some((i) => i.id === edit.id))
        next.items.push({
          id: edit.id,
          name: edit.name,
          quantity: edit.quantity,
          unit: edit.unit,
          category: "Household extras",
          state: "buy",
          manual: true,
          updated: false,
          contributions: [],
        });
      break;
  }
  return next;
}

export type QueuedShoppingEdit = {
  key: string;
  edit: ShoppingEdit;
  before?:
    | {
        quantity: string | null;
        state: ShoppingState;
        name: string;
        unit: string;
        manual: boolean;
      }
    | boolean;
};
export class ShoppingConflict extends Error {}
export function queueShoppingEdit(
  list: ShoppingList,
  edit: ShoppingEdit,
): QueuedShoppingEdit {
  const item = list.items.find((i) => i.id === edit.id);
  const before =
    edit.kind === "meal"
      ? list.meals.find((m) => m.position === edit.id)?.selected
      : item
        ? {
            quantity: item.quantity,
            state: item.state,
            name: item.name,
            unit: item.unit,
            manual: item.manual,
          }
        : undefined;
  return { key: crypto.randomUUID(), edit, before };
}
export function replayShopping(
  list: ShoppingList,
  queue: QueuedShoppingEdit[],
  force = false,
): ShoppingList {
  let result = structuredClone(list);
  for (const op of queue) {
    let edit = op.edit;
    const recoveredId = `recovered-${edit.id}`;
    let item = result.items.find((i) => i.id === edit.id);
    if (edit.kind === "state") {
      const before = typeof op.before === "object" ? op.before : undefined;
      if (!item && before && !before.manual) {
        item = result.items.find((i) => i.id === recoveredId && i.manual);
        if (item) edit = { ...edit, id: recoveredId };
      }
      if (!item) {
        if (!force || !before)
          throw new ShoppingConflict(
            `${before?.name ?? "A pending item"} was removed from the list. You can keep its pending shopping change as a household extra.`,
          );
        const id = before.manual ? edit.id : recoveredId;
        result = editShopping(result, {
          kind: "extra",
          id,
          name: before.name,
          quantity: before.quantity,
          unit: before.unit,
        });
        edit = { ...edit, id };
        item = result.items.find((i) => i.id === id)!;
      }
      if (
        !force &&
        (!before ||
          item.quantity !== before.quantity ||
          item.name !== before.name ||
          item.unit !== before.unit ||
          (item.state !== before.state && item.state !== edit.state))
      )
        throw new ShoppingConflict(
          `${item.name} changed on the server. Review its current amount and retry your changes.`,
        );
    }
    if (
      edit.kind === "extra" &&
      item &&
      (!item.manual ||
        item.name !== edit.name ||
        item.quantity !== edit.quantity ||
        item.unit !== edit.unit)
    ) {
      if (!force || !item.manual)
        throw new ShoppingConflict(
          "An extra item changed on another device. Review it before retrying.",
        );
      result.items = result.items.map((current) =>
        current.id === edit.id
          ? {
              ...current,
              name: edit.name,
              quantity: edit.quantity,
              unit: edit.unit,
            }
          : current,
      );
    }
    if (
      edit.kind === "remove" &&
      item &&
      !force &&
      typeof op.before === "object" &&
      (item.name !== op.before.name ||
        item.quantity !== op.before.quantity ||
        item.unit !== op.before.unit ||
        item.state !== op.before.state)
    )
      throw new ShoppingConflict(
        "An extra item changed on another device. Review it before retrying.",
      );
    if (edit.kind === "meal") {
      const meal = result.meals.find((m) => m.position === edit.id);
      if (!meal) {
        if (!force)
          throw new ShoppingConflict(
            "A selected meal position was removed. Review your current week.",
          );
        // Removed scheduled positions no longer carry a selection after replacement.
        continue;
      }
      if (
        !force &&
        meal.selected !== op.before &&
        meal.selected !== edit.selected
      )
        throw new ShoppingConflict("Meal selection changed on another device.");
    }
    result = editShopping(result, edit);
  }
  return result;
}
