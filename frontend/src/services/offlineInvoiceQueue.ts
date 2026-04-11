const STORAGE_KEY = 'gbos-pending-invoices';

export type PendingInvoicePayload = {
  invoiceBody: Record<string, unknown>;
  splitPayments?: { payment_mode: string; amount: number }[];
};

export type PendingInvoice = {
  id: string;
  payload: PendingInvoicePayload;
  createdAt: string;
};

function readAll(): PendingInvoice[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as PendingInvoice[];
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function writeAll(items: PendingInvoice[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(items));
}

export function getPendingInvoiceCount(): number {
  return readAll().length;
}

export function enqueuePendingInvoice(payload: PendingInvoicePayload): string {
  const id =
    typeof crypto !== 'undefined' && crypto.randomUUID
      ? crypto.randomUUID()
      : `inv-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
  const list = readAll();
  list.push({
    id,
    payload,
    createdAt: new Date().toISOString(),
  });
  writeAll(list);
  return id;
}

export function removePendingInvoice(id: string) {
  writeAll(readAll().filter((p) => p.id !== id));
}

export async function flushPendingInvoiceQueue(
  createInvoice: (body: Record<string, unknown>) => Promise<{ id: string }>,
  addSplitPayments?: (
    invoiceId: string,
    payments: { payment_mode: string; amount: number }[]
  ) => Promise<void>
): Promise<{ flushed: number; failed: number }> {
  const pending = readAll();
  let flushed = 0;
  let failed = 0;
  for (const item of pending) {
    try {
      const inv = await createInvoice(item.payload.invoiceBody);
      const splits = item.payload.splitPayments;
      if (splits && splits.length > 0 && addSplitPayments) {
        await addSplitPayments(inv.id, splits);
      }
      removePendingInvoice(item.id);
      flushed++;
    } catch {
      failed++;
      break;
    }
  }
  return { flushed, failed };
}
