import { backoffMs } from './sync';

/**
 * The delay after repeated failures. It exists for the connection a shop
 * actually has — a phone hotspot that comes and goes — where retrying every
 * second helps neither the till nor the line.
 */
describe('backoffMs', () => {
  it('grows with consecutive failures', () => {
    const first = median(() => backoffMs(1));
    const third = median(() => backoffMs(3));
    const fifth = median(() => backoffMs(5));
    expect(third).toBeGreaterThan(first);
    expect(fifth).toBeGreaterThan(third);
  });

  it('caps at about a minute, so a shop back online is not still waiting', () => {
    for (const failures of [6, 10, 50, 1000]) {
      expect(backoffMs(failures)).toBeLessThanOrEqual(60_000 * 1.25 + 1);
    }
  });

  it('never returns zero or a negative delay', () => {
    for (const failures of [0, -1, 1, 2, 7]) {
      expect(backoffMs(failures)).toBeGreaterThan(0);
    }
  });

  it('spreads several tills apart rather than retrying in lockstep', () => {
    const samples = new Set(Array.from({ length: 40 }, () => backoffMs(3)));
    // Jitter, so a shop with four counters does not send four requests at the
    // same instant on a connection that just came back.
    expect(samples.size).toBeGreaterThan(1);
  });
});

function median(f: () => number): number {
  const values = Array.from({ length: 21 }, f).sort((a, b) => a - b);
  return values[10];
}
