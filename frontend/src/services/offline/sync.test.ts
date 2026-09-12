import { backoffMs, classify } from './sync';

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


/**
 * What a failure means, and what the till does about it.
 *
 * This is where a sale was lost. A 409 used to count as "the server already has
 * it", so the queue entry was deleted — but a replayed sale does not come back
 * as a conflict, it comes back as the original sale with a 201, because that is
 * what the idempotency key is for. A 409 means something genuinely clashed,
 * almost always that the number the till allocated is on another bill, and
 * deleting the entry took the sale with it.
 */
describe('classify', () => {
  const withStatus = (status: number, body?: any) => ({ response: { status, data: body } });

  it('does not treat a conflict as success', () => {
    const { permanent, message } = classify(
      withStatus(409, { error: 'that invoice number is already in use' })
    );
    // Permanent: a duplicate number does not resolve itself, so the sale waits
    // for a person rather than retrying for ever or being dropped.
    expect(permanent).toBe(true);
    expect(message).toContain('already in use');
  });

  it('keeps a sale queued when there is no response at all', () => {
    expect(classify({ message: 'Network Error' }).permanent).toBe(false);
    expect(classify({}).permanent).toBe(false);
  });

  it('keeps a sale queued when the server is having a bad moment', () => {
    for (const status of [500, 502, 503, 504]) {
      expect(classify(withStatus(status)).permanent).toBe(false);
    }
  });

  it('keeps a sale queued when it is only being asked to wait', () => {
    for (const status of [408, 425, 429]) {
      expect(classify(withStatus(status)).permanent).toBe(false);
    }
  });

  it('stops on a request the server will never accept', () => {
    for (const status of [400, 401, 403, 404, 422]) {
      expect(classify(withStatus(status)).permanent).toBe(true);
    }
  });

  it('reports what the server said, so a person can act on it', () => {
    expect(classify(withStatus(422, { error: 'product is no longer sold' })).message).toBe(
      'product is no longer sold'
    );
    expect(classify(withStatus(422, { message: 'unit has no price' })).message).toBe('unit has no price');
  });
});
