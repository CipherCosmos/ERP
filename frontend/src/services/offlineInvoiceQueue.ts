const STORAGE_KEY = 'gbos-pending-invoices';

export type PendingInvoicePayload = {
  invoiceBody: Record<string, unknown>;
  splitPayments?: { payment_mode: string; amount: number }[];
};

export type PendingInvoice = {
  id: string;
  payload: PendingInvoicePayload;
  createdAt: string;
  /** If set, the invoice was already created on the server (only splits remain). */
  createdInvoiceId?: string;
  retryCount?: number;
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
    retryCount: 0,
  });
  writeAll(list);
  return id;
}

export function removePendingInvoice(id: string) {
  writeAll(readAll().filter((p) => p.id !== id));
}

const MAX_RETRIES = 5;

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
    // Skip items that have failed too many times
    if ((item.retryCount ?? 0) >= MAX_RETRIES) {
      failed++;
      continue;
    }

    try {
      let invoiceId = item.createdInvoiceId;

      // Step 1: Create invoice (only if not already created)
      if (!invoiceId) {
        const inv = await createInvoice(item.payload.invoiceBody);
        invoiceId = inv.id;
        // Persist the created invoice ID so we don't re-create on retry
        const all = readAll();
        const idx = all.findIndex((p) => p.id === item.id);
        if (idx >= 0) {
          all[idx].createdInvoiceId = invoiceId;
          writeAll(all);
        }
      }

      // Step 2: Add split payments (if any)
      const splits = item.payload.splitPayments;
      if (splits && splits.length > 0 && addSplitPayments) {
        try {
          await addSplitPayments(invoiceId, splits);
        } catch (splitErr: any) {
          // If splits fail with 409 (already exists), treat as success
          if (splitErr?.response?.status === 409) {
            // Already recorded, proceed
          } else {
            throw splitErr;
          }
        }
      }

      removePendingInvoice(item.id);
      flushed++;
    } catch {
      // Increment retry count
      const all = readAll();
      const idx = all.findIndex((p) => p.id === item.id);
      if (idx >= 0) {
        all[idx].retryCount = (all[idx].retryCount ?? 0) + 1;
        writeAll(all);
      }
      failed++;
      // Continue to next item instead of breaking
    }
  }
  return { flushed, failed };
}
