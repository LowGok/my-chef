// The runner event is authorization for one unsigned CI lane, not activation.
const object = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown> : null;

export function authorizedAnalyticsCI(input: unknown, environment: Record<string, string | undefined>): boolean {
  const event = object(input), repository = object(event?.repository), sender = object(event?.sender);
  const revision = environment.GITHUB_SHA;
  return !!event && !!repository && !!sender && typeof revision === 'string' && /^[a-f0-9]{40}$/.test(revision)
    && environment.GITHUB_ACTIONS === 'true' && environment.GITHUB_EVENT_NAME === 'push'
    && environment.GITHUB_REPOSITORY === 'LowGok/my-chef' && environment.GITHUB_ACTOR === 'LowGok'
    && repository.id === 1454481532 && repository.full_name === 'LowGok/my-chef' && repository.private === false
    && sender.login === 'LowGok' && event.deleted === false
    && event.ref === 'refs/heads/ci/analytics-installed-b9e9699' && environment.GITHUB_REF === event.ref
    && event.after === revision && ['android', 'ios-simulator'].includes(environment.NATIVE_PLATFORM ?? '');
}
