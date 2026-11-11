import type { ShoppingItem } from "./shopping-state";
export type ShoppingOrder = "category" | "fresh" | "name" | "quantity";
const compare = (a: ShoppingItem, b: ShoppingItem) =>
  a.name.localeCompare(b.name, "en", { sensitivity: "base", numeric: true }) ||
  a.id.localeCompare(b.id);
function amount(value: string | null): [bigint, bigint] | null {
  if (!value || value.length > 100 || !/^\d+(?:\.\d+|\/[1-9]\d*)?$/.test(value))
    return null;
  if (value.includes("/")) {
    const [a, b] = value.split("/");
    return [BigInt(a), BigInt(b)];
  }
  const [whole, fraction = ""] = value.split(".");
  return [BigInt(whole + fraction), 10n ** BigInt(fraction.length)];
}
function unit(item: ShoppingItem) {
  const raw = item.unit.trim().toLowerCase();
  if (["g", "gram", "grams", "kg", "kilogram", "kilograms"].includes(raw))
    return {
      key: "g",
      label: "Weight (g)",
      scale: raw.startsWith("k") ? 1000n : 1n,
    };
  if (
    [
      "ml",
      "millilitre",
      "millilitres",
      "milliliter",
      "milliliters",
      "l",
      "litre",
      "litres",
      "liter",
      "liters",
    ].includes(raw)
  )
    return {
      key: "ml",
      label: "Volume (ml)",
      scale: ["l", "litre", "litres", "liter", "liters"].includes(raw)
        ? 1000n
        : 1n,
    };
  if (["piece", "pieces"].includes(raw))
    return { key: "piece", label: "Pieces", scale: 1n };
  return { key: raw, label: raw || "Unspecified unit", scale: 1n };
}
function freshness(item: ShoppingItem) {
  const text = (item.category + " " + item.name).toLowerCase();
  if (/frozen/.test(text)) return 2;
  if (
    /canned|tinned|dried|powder(?:ed)?|sauce|flour|noodles|pasta|(?:peanut|almond|cashew|nut|cocoa) butter|shelf.stable|uht|tins & jars|grains & bread|oils & seasonings/.test(
      text,
    )
  )
    return 3;
  if (/fruit & vegetables|fresh produce/.test(text)) return 0;
  if (
    /\b(milk|yogurt|yoghurt|cheese|cream|butter|eggs?|chicken|beef|salmon|fish)\b|chilled|dairy/.test(
      text,
    )
  )
    return 1;
  return 3;
}
export function orderShopping(
  items: ShoppingItem[],
  tab: "buy" | "home",
  mode: ShoppingOrder,
) {
  const basket =
    tab === "buy"
      ? items.filter((i) => i.state === "basket").sort(compare)
      : [];
  const selected = items.filter(
    (i) => i.state === (tab === "home" ? "home" : "buy"),
  );
  selected.sort((a, b) => {
    if (mode === "category")
      return a.category.localeCompare(b.category) || compare(a, b);
    if (mode === "fresh") return freshness(a) - freshness(b) || compare(a, b);
    if (mode === "quantity") {
      const x = unit(a),
        y = unit(b),
        byUnit = x.key.localeCompare(y.key);
      if (byUnit) return byUnit;
      const aq = amount(a.quantity),
        bq = amount(b.quantity);
      if (!aq || !bq) return aq ? -1 : bq ? 1 : compare(a, b);
      const delta = aq[0] * x.scale * bq[1] - bq[0] * y.scale * aq[1];
      return delta > 0n ? -1 : delta < 0n ? 1 : compare(a, b);
    }
    return compare(a, b);
  });
  const groups = new Map<
    string,
    { key: string; label: string; items: ShoppingItem[] }
  >();
  for (const item of selected) {
    const bucket =
      mode === "category"
        ? { key: item.category, label: item.category }
        : mode === "fresh"
          ? {
              key: String(freshness(item)),
              label: [
                "Fresh produce",
                "Chilled foods",
                "Frozen foods",
                "Other items",
              ][freshness(item)],
            }
          : mode === "quantity"
            ? unit(item)
            : { key: "name", label: "" };
    if (!groups.has(bucket.key))
      groups.set(bucket.key, {
        key: bucket.key,
        label: bucket.label,
        items: [],
      });
    groups.get(bucket.key)!.items.push(item);
  }
  return { groups: [...groups.values()], basket };
}
