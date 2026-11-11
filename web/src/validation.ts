export function validDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const date = new Date(`${value}T00:00:00Z`);
  return (
    !Number.isNaN(date.valueOf()) && date.toISOString().slice(0, 10) === value
  );
}

export function menuInputError(
  dates: string[],
  adults: number,
  children: number,
  minutes: number,
): string | null {
  if (
    dates.length < 1 ||
    dates.length > 7 ||
    dates.some((date) => !validDate(date)) ||
    new Set(dates).size !== dates.length
  )
    return "Choose one to seven distinct meal dates.";
  if (
    !Number.isInteger(adults) ||
    adults < 1 ||
    adults > 20 ||
    !Number.isInteger(children) ||
    children < 0 ||
    children > 20
  )
    return "Choose a household of 1–20 adults and 0–20 children.";
  if (!Number.isInteger(minutes) || minutes < 10 || minutes > 240)
    return "Choose a time limit between 10 and 240 minutes.";
  return null;
}
