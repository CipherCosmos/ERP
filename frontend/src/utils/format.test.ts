import { addDaysISO, daysUntilISO, thisMonthISO, toISODate, todayISO } from './format';

// These are calendar dates, not instants. The distinction is the whole point:
// every screen used to compute them with toISOString, which converts to UTC
// first, and India is five and a half hours ahead of it.
describe('the shop calendar', () => {
  it('renders a local date without going through UTC', () => {
    // Midnight on the 14th in any timezone east of Greenwich is the evening of
    // the 13th in UTC, so toISOString would say the 13th.
    expect(toISODate(new Date(2026, 8, 14, 0, 0, 0))).toBe('2026-09-14');
    // And late evening is already the next day in UTC for timezones west of it.
    expect(toISODate(new Date(2026, 8, 14, 23, 59, 0))).toBe('2026-09-14');
  });

  it('pads months and days', () => {
    expect(toISODate(new Date(2026, 0, 5))).toBe('2026-01-05');
  });

  it('counts days as a calendar does', () => {
    // The case that was wrong on the receiving screen: packed on the 11th with
    // a three day shelf life expires on the 14th, and the server agrees.
    expect(addDaysISO('2026-09-11', 3)).toBe('2026-09-14');
    expect(addDaysISO('2026-09-11T00:00:00Z', 3)).toBe('2026-09-14');
    // Month and year ends.
    expect(addDaysISO('2026-01-31', 1)).toBe('2026-02-01');
    expect(addDaysISO('2026-12-31', 1)).toBe('2027-01-01');
    // A leap year, because 2028 is one.
    expect(addDaysISO('2028-02-28', 1)).toBe('2028-02-29');
    expect(addDaysISO('2026-03-01', -1)).toBe('2026-02-28');
  });

  it('refuses what is not a date rather than inventing one', () => {
    expect(addDaysISO('', 1)).toBe('');
    expect(addDaysISO('not a date', 1)).toBe('');
    expect(daysUntilISO(null)).toBeNull();
    expect(daysUntilISO('not a date')).toBeNull();
  });

  it('measures days to a date from the start of today', () => {
    const now = new Date();
    const today = toISODate(now);
    expect(daysUntilISO(today)).toBe(0);
    expect(daysUntilISO(addDaysISO(today, 3))).toBe(3);
    expect(daysUntilISO(addDaysISO(today, -2))).toBe(-2);
    // Independent of the time of day, which is why both ends are truncated to
    // a calendar date before the subtraction.
    expect(daysUntilISO(today + 'T23:59:59Z')).toBe(0);
  });

  it('agrees with itself', () => {
    expect(todayISO()).toBe(toISODate(new Date()));
  });
});

// The GST return screen opens on this, and a statutory return prepared for the
// wrong month is not a mistake anybody catches by looking at the screen.
describe('the current month', () => {
  it('is the local month, not the UTC one', () => {
    const now = new Date();
    const pad = (n: number) => String(n).padStart(2, '0');
    expect(thisMonthISO()).toBe(`${now.getFullYear()}-${pad(now.getMonth() + 1)}`);
  });
});
