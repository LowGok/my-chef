import { afterEach, describe, expect, setSystemTime, test } from "bun:test";
import { assessAudit } from "./audit-web";

const advisory = {
  url: "https://github.com/advisories/GHSA-g3hv-75wg-m249",
  severity: "moderate",
  vulnerable_versions: "<11.0.1",
};
const lock = { packages: { minimatch: ["minimatch@5.1.9"] } };
afterEach(() => setSystemTime());

describe("strict web dependency audit", () => {
  for (const time of [
    "2026-10-31T12:00:00Z",
    "2026-11-15T00:00:00Z",
    "2027-01-01T00:00:00Z",
  ]) {
    test(`clean audit passes and all findings fail at ${time}`, () => {
      setSystemTime(new Date(time));
      expect(assessAudit({}, 0, lock)).toEqual({ exceptions: 0 });
      expect(() => assessAudit({ minimatch: [advisory] }, 1, lock)).toThrow();
      expect(() =>
        assessAudit({ other: [{ ...advisory, severity: "low" }] }, 0, lock),
      ).toThrow();
    });
  }
  test("malformed reports and unexplained failure exit codes fail closed", () => {
    for (const report of [
      null,
      [],
      "bad",
      { minimatch: [] },
      { minimatch: [null] },
      { minimatch: [{}] },
    ]) {
      expect(() => assessAudit(report, 1, lock)).toThrow();
    }
    for (const code of [null, 1, 2])
      expect(() => assessAudit({}, code, lock)).toThrow();
  });
});
