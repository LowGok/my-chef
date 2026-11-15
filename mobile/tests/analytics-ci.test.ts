import { expect, test } from 'bun:test';
import { authorizedAnalyticsCI } from './analytics-ci';

const event = {
  repository: { id: 1454481532, full_name: 'LowGok/my-chef', private: false },
  sender: { login: 'LowGok' }, ref: 'refs/heads/ci/analytics-installed-b9e9699',
  after: 'a'.repeat(40), deleted: false,
};
const environment = {
  GITHUB_ACTIONS: 'true', GITHUB_EVENT_NAME: 'push', GITHUB_REPOSITORY: 'LowGok/my-chef',
  GITHUB_ACTOR: 'LowGok', GITHUB_REF: event.ref, GITHUB_SHA: event.after, NATIVE_PLATFORM: 'android',
};

test('only the owner push at its exact ref and revision can build unsigned analytics', () => {
  for (const platform of ['android', 'ios-simulator'])
    expect(authorizedAnalyticsCI(event, { ...environment, NATIVE_PLATFORM: platform })).toBe(true);
  for (const [key, value] of Object.entries({
    GITHUB_ACTIONS: 'false', GITHUB_EVENT_NAME: 'pull_request_target', GITHUB_REPOSITORY: 'Other/my-chef',
    GITHUB_ACTOR: 'Other', GITHUB_REF: 'refs/heads/main', GITHUB_SHA: 'b'.repeat(40), NATIVE_PLATFORM: 'ios-device',
  })) expect(authorizedAnalyticsCI(event, { ...environment, [key]: value })).toBe(false);
  for (const changes of [
    { repository: { ...event.repository, id: 1 } }, { repository: { ...event.repository, private: true } },
    { repository: { ...event.repository, full_name: 'Other/my-chef' } }, { sender: { login: 'Other' } },
    { ref: 'refs/heads/main' }, { after: 'b'.repeat(40) }, { deleted: true },
  ]) expect(authorizedAnalyticsCI({ ...event, ...changes }, environment)).toBe(false);
});

test('empty, coerced or malformed event values are never authority', () => {
  for (const after of ['', 'a'.repeat(39), 'A'.repeat(40), 'g'.repeat(40), undefined])
    expect(authorizedAnalyticsCI({ ...event, after }, { ...environment, GITHUB_SHA: after })).toBe(false);
  for (const payload of [null, [], true, {}, { repository: null }, { ...event, sender: null }])
    expect(authorizedAnalyticsCI(payload, environment)).toBe(false);
});
