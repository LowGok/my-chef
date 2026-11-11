import { expect, test } from "bun:test";
import { editShopping, type ShoppingList } from "./shopping-state";

const list: ShoppingList = {
  week: "2026-11-02",
  revision: 1,
  sourceVersion: "source",
  meals: [
    {
      position: "2026-11-02/0",
      recipeId: "1",
      date: "2026-11-02",
      title: "Soup",
      selected: true,
    },
  ],
  items: [
    {
      id: "carrot",
      name: "carrot",
      quantity: "450",
      unit: "g",
      category: "Fruit & vegetables",
      state: "buy",
      updated: false,
      manual: false,
      contributions: [],
    },
  ],
};
test("shopping edits preserve source fields and support reversible extras", () => {
  const basket = editShopping(list, {
    kind: "state",
    id: "carrot",
    state: "basket",
  });
  expect(basket.items[0].state).toBe("basket");
  expect(list.items[0].state).toBe("buy");
  const home = editShopping(basket, {
    kind: "state",
    id: "carrot",
    state: "home",
  });
  expect(home.items[0].state).toBe("home");
  const extra = editShopping(home, {
    kind: "extra",
    id: "extra-id",
    name: "Dish soap",
    quantity: "1",
    unit: "bottle",
  });
  expect(extra.items[1]).toMatchObject({
    id: "extra-id",
    manual: true,
    name: "Dish soap",
  });
  expect(
    editShopping(extra, { kind: "remove", id: "carrot" }).items,
  ).toHaveLength(2);
  expect(
    editShopping(extra, { kind: "remove", id: "extra-id" }).items,
  ).toHaveLength(1);
  expect(
    editShopping(list, { kind: "meal", id: "2026-11-02/0", selected: false })
      .meals[0].selected,
  ).toBe(false);
});

import {
  queueShoppingEdit,
  replayShopping,
  ShoppingConflict,
} from "./shopping-state";

test("offline edits rebase on refreshed meals without losing extras or duplicating acknowledged saves", () => {
  const first = queueShoppingEdit(list, {
    kind: "state",
    id: "carrot",
    state: "basket",
  });
  const extra = queueShoppingEdit(list, {
    kind: "extra",
    id: "soap",
    name: "Dish soap",
    quantity: "1",
    unit: "bottle",
  });
  const updated = {
    ...structuredClone(list),
    revision: 2,
    sourceVersion: "replacement",
  };
  const merged = replayShopping(updated, [first, extra]);
  expect(merged.items[0].state).toBe("basket");
  expect(merged.items[1].id).toBe("soap");
  expect(replayShopping(merged, [first, extra]).items).toHaveLength(2);
  const changed = structuredClone(updated);
  changed.items[0].quantity = "500";
  expect(() => replayShopping(changed, [first, extra])).toThrow(
    ShoppingConflict,
  );
  expect(replayShopping(changed, [first, extra], true).items[0]).toMatchObject({
    state: "basket",
    quantity: "500",
  });
  const otherDevice = structuredClone(updated);
  otherDevice.items[0].state = "home";
  expect(() => replayShopping(otherDevice, [first])).toThrow(ShoppingConflict);
  expect(replayShopping(otherDevice, [first], true).items[0].state).toBe(
    "basket",
  );
});

test("explicit removed-source recovery preserves the original edit and later extras once", () => {
  const original = structuredClone(list);
  const queue = [
    queueShoppingEdit(original, {
      kind: "state",
      id: "carrot",
      state: "basket",
    }),
    queueShoppingEdit(original, {
      kind: "extra",
      id: "later-extra",
      name: "Coffee",
      quantity: "1",
      unit: "bag",
    }),
  ];
  const replacement = {
    ...structuredClone(original),
    revision: 2,
    sourceVersion: "removed",
    items: [],
  };
  expect(() => replayShopping(replacement, queue)).toThrow(ShoppingConflict);
  const recovered = replayShopping(replacement, queue, true);
  expect(recovered.items).toHaveLength(2);
  expect(recovered.items[0]).toMatchObject({
    id: "recovered-carrot",
    name: "carrot",
    quantity: "450",
    unit: "g",
    manual: true,
    state: "basket",
  });
  expect(recovered.items[1].name).toBe("Coffee");
  expect(replayShopping(recovered, queue, true).items).toHaveLength(2);
});

test("reviewed retries resolve superseded meal choices and changed manual extras", () => {
  const removed = { ...structuredClone(list), meals: [] };
  const choice = queueShoppingEdit(list, {
    kind: "meal",
    id: list.meals[0].position,
    selected: false,
  });
  expect(() => replayShopping(removed, [choice])).toThrow(ShoppingConflict);
  expect(replayShopping(removed, [choice], true).meals).toHaveLength(0);
  const added = queueShoppingEdit(list, {
    kind: "extra",
    id: "soap",
    name: "Dish soap",
    quantity: "1",
    unit: "bottle",
  });
  const remote = editShopping(list, {
    kind: "extra",
    id: "soap",
    name: "Changed soap",
    quantity: "2",
    unit: "bottles",
  });
  expect(() => replayShopping(remote, [added])).toThrow(ShoppingConflict);
  expect(
    replayShopping(remote, [added], true).items.find((i) => i.id === "soap"),
  ).toMatchObject({ name: "Dish soap", quantity: "1", unit: "bottle" });
});
