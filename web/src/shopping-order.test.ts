import { expect, test } from "bun:test";
import { orderShopping } from "./shopping-order";
import type { ShoppingItem } from "./shopping-state";
const item = (
  name: string,
  quantity: string | null,
  unit: string,
  category = "Other ingredients",
  state: ShoppingItem["state"] = "buy",
): ShoppingItem => ({
  id: name,
  name,
  quantity,
  unit,
  category,
  state,
  manual: false,
  updated: false,
  contributions: [],
});
const fixtures = [
  item("Rice", "100", "g", "Grains & bread"),
  item("Spinach", "50", "g", "Fruit & vegetables"),
  item("Milk", "500", "ml"),
  item("Frozen peas", "200", "g", "Frozen"),
  item("Soap", null, "bottle"),
  item("Coffee", "1", "bag", "Household extras", "basket"),
  item("Salt", "2", "g", "Oils & seasonings", "home"),
];
test("every mode partitions completed purchases independently of sorting and home items", () => {
  for (const mode of ["category", "fresh", "name", "quantity"] as const) {
    const result = orderShopping(fixtures, "buy", mode);
    expect(result.basket.map((i) => i.name)).toEqual(["Coffee"]);
    expect(
      result.groups
        .flatMap((g) => g.items)
        .map((i) => i.name)
        .sort(),
    ).toEqual(["Frozen peas", "Milk", "Rice", "Soap", "Spinach"].sort());
    expect(
      orderShopping(fixtures, "home", mode)
        .groups.flatMap((g) => g.items)
        .map((i) => i.name),
    ).toEqual(["Salt"]);
  }
});
test("fresh priority and global alphabetic order are deterministic", () => {
  expect(
    orderShopping(fixtures, "buy", "fresh")
      .groups.flatMap((g) => g.items)
      .map((i) => i.name),
  ).toEqual(["Spinach", "Milk", "Frozen peas", "Rice", "Soap"]);
  expect(
    orderShopping(fixtures, "buy", "name")
      .groups.flatMap((g) => g.items)
      .map((i) => i.name),
  ).toEqual(["Frozen peas", "Milk", "Rice", "Soap", "Spinach"]);
});
test("quantity compares exact fractions and normalized metric amounts only within matching units", () => {
  const values = [
    item("A", "0.10000000000000000000000000001", "g"),
    item("B", "0.1", "g"),
    item("C", "1/2", "g"),
    item("D", "0.001", "kg"),
    item("E", "999", "ml"),
    item("F", "to taste", "g"),
  ];
  const groups = orderShopping(values, "buy", "quantity").groups;
  expect(groups.find((g) => g.key === "g")?.items.map((i) => i.name)).toEqual([
    "D",
    "C",
    "A",
    "B",
    "F",
  ]);
  expect(groups.find((g) => g.key === "ml")?.items.map((i) => i.name)).toEqual([
    "E",
  ]);
  expect(values.find((i) => i.name === "D")?.quantity).toBe("0.001");
});

test("shelf-stable compound names do not inherit chilled priority", () => {
  const foods = [
    item("fish sauce", "1", "bottle"),
    item("milk powder", "100", "g"),
    item("peanut butter", "100", "g"),
    item("egg noodles", "100", "g"),
    item("Fresh salmon", "100", "g"),
    item("Milk", "500", "ml"),
    item("Frozen peas", "100", "g", "Frozen"),
  ];
  const groups = orderShopping(foods, "buy", "fresh").groups;
  expect(
    groups
      .find((group) => group.label === "Chilled foods")
      ?.items.map((i) => i.name),
  ).toEqual(["Fresh salmon", "Milk"]);
  expect(
    groups
      .flatMap((group) => group.items)
      .findIndex((i) => i.name === "fish sauce"),
  ).toBeGreaterThan(
    groups
      .flatMap((group) => group.items)
      .findIndex((i) => i.name === "Frozen peas"),
  );
  expect(
    groups
      .find((group) => group.label === "Other items")
      ?.items.map((i) => i.name),
  ).toEqual(["egg noodles", "fish sauce", "milk powder", "peanut butter"]);
});
