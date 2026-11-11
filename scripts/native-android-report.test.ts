import { expect, test } from "bun:test";
import { safeJourneyError } from "./native-android-report";

test("native journey diagnostics retain a useful error without private values", () => {
  const password = "A private synthetic password!";
  const error = new Error(
    `locator.fill: ${password} user@example.test Bearer ${"a".repeat(64)} https://localhost/app/reset-password#${"b".repeat(43)}\nSecret second line`,
  );
  error.stack =
    "Error\n at /home/runner/project/scripts/native-android-journeys.ts:95:51";
  const result = safeJourneyError(error, "native sign-in", [password]);
  expect(result).toContain("locator.fill:");
  expect(result).toContain("native-android-journeys.ts:95:51");
  for (const value of [
    password,
    "user@example.test",
    "a".repeat(64),
    "b".repeat(43),
    "Secret second line",
    "/home/runner",
  ])
    expect(result).not.toContain(value);
});

test("unexpected diagnostic objects never stringify private payloads", () => {
  const result = safeJourneyError(
    {
      password: "private",
      toString() {
        throw Error("do not serialize");
      },
    },
    "unsafe\nstage",
  );
  expect(result).toBe(
    "Installed Android journey failed at stage: unknown; Failure",
  );
});
