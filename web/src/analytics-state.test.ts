import { expect, test } from "bun:test";
import {
  analyticsRevision,
  clearAnalyticsRevision,
  rememberAnalyticsRevision,
  queuedAnalyticsRevision,
} from "./analytics-state";
test("queued operations retain admission revisions across opt-out and re-enrollment", () => {
  clearAnalyticsRevision();
  expect(analyticsRevision()).toBeNull();
  rememberAnalyticsRevision(1);
  const old = [{ analyticsRevision: analyticsRevision() }];
  rememberAnalyticsRevision(3);
  expect(queuedAnalyticsRevision(old)).toBe(1);
  expect(queuedAnalyticsRevision([...old, { analyticsRevision: 3 }])).toBe(0);
  expect(queuedAnalyticsRevision([{}])).toBe(0);
  clearAnalyticsRevision();
  expect(analyticsRevision()).toBeNull();
});
