import { expect, test } from "bun:test";
import { menuInputError, validDate } from "./validation";

test("meal dates validate actual calendar boundaries", () => {
  expect(validDate("2026-02-29")).toBe(false);
  expect(validDate("2028-02-29")).toBe(true);
  expect(menuInputError(["2026-12-31", "2027-01-01"], 2, 1, 30)).toBeNull();
});
test("incomplete or duplicate requests cannot start generation", () => {
  expect(menuInputError([], 2, 0, 30)).not.toBeNull();
  expect(menuInputError(["2026-10-28", "2026-10-28"], 2, 0, 30)).not.toBeNull();
  expect(menuInputError(["2026-10-28"], 0, 0, 30)).not.toBeNull();
  expect(menuInputError(["2026-10-28"], 2, 0, 9)).not.toBeNull();
});
