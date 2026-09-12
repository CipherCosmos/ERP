import { financialYear, formatNumber } from './numbering';

/**
 * The till and the server must compute the same financial year from the same
 * instant, or a sale is refused on sync for claiming a year it does not fall in.
 *
 * The year turns at Indian midnight on 1 April. A till left on the wrong
 * timezone — a tablet bought abroad, a browser in UTC — must still agree, which
 * is why the offset is applied to the UTC instant and never to a local reading.
 */
describe('financialYear', () => {
  const cases: Array<[string, string]> = [
    // The moment the year turns, in IST, expressed as UTC.
    ['2026-03-31T18:29:59Z', '2025-26'], // 23:59:59 IST on 31 March
    ['2026-03-31T18:30:00Z', '2026-27'], // 00:00:00 IST on 1 April
    ['2026-03-31T23:30:00Z', '2026-27'], // 05:00 IST on 1 April — the old bug's window
    ['2026-04-01T00:00:00Z', '2026-27'],
    ['2027-03-31T18:29:00Z', '2026-27'],
    ['2027-03-31T18:31:00Z', '2027-28'],
    ['2026-01-15T12:00:00Z', '2025-26'], // January belongs to the year that began last April
    ['2026-09-12T12:00:00Z', '2026-27'],
  ];

  it.each(cases)('%s falls in %s', (instant, want) => {
    expect(financialYear(new Date(instant))).toBe(want);
  });

  it('does not depend on the device timezone', () => {
    // Two Date objects for the same instant, however the host is configured,
    // have the same getTime, so the answer cannot vary with it.
    const instant = new Date('2026-03-31T18:30:00Z');
    expect(financialYear(instant)).toBe(financialYear(new Date(instant.getTime())));
  });
});

/**
 * The number has to render exactly as the server renders it. The server parses
 * what arrives and refuses anything it would not itself have produced, so an
 * unpadded sequence or a missing part is a sale the till cannot sync.
 */
describe('formatNumber', () => {
  it('pads the sequence to five places', () => {
    expect(formatNumber('T1', '2026-27', 1)).toBe('INV/T1/2026-27/00001');
    expect(formatNumber('T1', '2026-27', 42)).toBe('INV/T1/2026-27/00042');
    expect(formatNumber('T2', '2026-27', 99999)).toBe('INV/T2/2026-27/99999');
  });

  it('does not truncate a series that runs past five digits', () => {
    expect(formatNumber('T1', '2026-27', 100000)).toBe('INV/T1/2026-27/100000');
  });
});
