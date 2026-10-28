import axios, { AxiosResponse } from 'axios';
import {
  Category,
  CategoryMergeResult,
  ImportReport,
  PriceMovement,
  Product,
  Store,
  Customer,
  Invoice,
  ReorderItem,
  Terminal,
  UnitFamily,
  UnitOfMeasure,
} from '../types';

const api = axios.create({
  baseURL: process.env.REACT_APP_API_URL || '/api/v1',
});

api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token');
  const storeId = localStorage.getItem('storeId');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  if (storeId) {
    config.headers['X-Store-ID'] = storeId;
  }
  return config;
});

/**
 * The server reports a failure as a structured envelope:
 *
 *   {"error": {"code": "validation_failed",
 *              "message": "The request contains invalid fields.",
 *              "fields": {"name": "this field is required"}}}
 *
 * Screens read `err.response.data.error` and put it straight into a message
 * string or a JSX expression. An object there renders as `[object Object]` in
 * an alert, and in JSX React refuses it outright — "Objects are not valid as a
 * React child" — which unmounts the page. So the Settings screen white-screened
 * on exactly the validation errors it exists to show.
 *
 * Flattening it once here fixes all six call sites, and keeps the field-level
 * detail: a caller that wants the structure still has it on `error.details`.
 */
// Words the trade spells in capitals. Title-casing them would produce "Gst
// rate", which reads as a typo to anyone who files a return.
const ACRONYMS = new Set([
  'gst', 'cgst', 'sgst', 'igst', 'hsn', 'sac', 'uom', 'mrp', 'sku',
  'gstin', 'upi', 'po', 'grn', 'qty', 'id',
]);

/**
 * Turns a wire field name into something that reads like a form label:
 * `family_id` becomes "Family", `gst_rate` becomes "GST rate". A trailing
 * `_id` is dropped because the person filling the form picks a category, not
 * a category's identifier.
 */
export function humanizeField(name: string): string {
  const words = name
    .replace(/_id$/, '')
    .split(/[_.]/)
    .filter(Boolean);
  if (words.length === 0) return name;
  return words
    .map((word, i) => {
      if (ACRONYMS.has(word.toLowerCase())) return word.toUpperCase();
      if (i === 0) return word.charAt(0).toUpperCase() + word.slice(1);
      return word;
    })
    .join(' ');
}

function describeError(payload: any): string {
  if (typeof payload === 'string') return payload;
  if (!payload || typeof payload !== 'object') return '';

  const parts: string[] = [];
  if (typeof payload.message === 'string' && payload.message) {
    parts.push(payload.message);
  }
  // Field errors are the actionable half — "Name is required" is what the user
  // has to act on, and the generic message above never carries it.
  const fields = payload.fields;
  if (fields && typeof fields === 'object') {
    const detail = Object.entries(fields)
      .map(([name, problem]) => `${humanizeField(name)} ${problem}`)
      .join(', ');
    if (detail) parts.push(detail);
  }
  return parts.join(' — ');
}

api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token');
      localStorage.removeItem('storeId');
      // Not if we are already there. The login screen still renders components
      // that load data, and a persisted session whose token has expired makes
      // each of those 401 — every one of which used to reassign location and
      // reload the page, which loaded them again. Measured on a real tab before
      // the guard: eight thousand requests and a rate-limited server, from a
      // till nobody had touched.
      if (window.location.pathname !== '/login') {
        window.location.href = '/login';
      }
    }
    const data = error.response?.data;
    if (data && typeof data === 'object' && 'error' in data) {
      const described = describeError(data.error);
      if (described) {
        // Keep the original structure reachable rather than discarding it.
        data.details = data.error;
        data.error = described;
      }
    }
    return Promise.reject(error);
  }
);

/**
 * The API returns every collection inside a named envelope, e.g.
 * `{"products": [...]}` rather than a bare `[...]`. Screens read `data` as the
 * collection itself, so the unwrapping happens here — in the one place every
 * response passes through — instead of in each of the twenty-nine pages.
 *
 * A missing key yields an empty list rather than undefined, so a screen that
 * maps over the result cannot crash on an unexpected response.
 */
function list<T>(request: Promise<AxiosResponse<any>>, key: string): Promise<AxiosResponse<T[]>> {
  return request.then((res) => ({
    ...res,
    data: Array.isArray(res.data) ? res.data : (res.data?.[key] ?? []),
  }));
}

/** field unwraps a single named value from a response envelope. */
function field<T>(request: Promise<AxiosResponse<any>>, key: string): Promise<AxiosResponse<T>> {
  return request.then((res) => ({
    ...res,
    data: res.data?.[key] !== undefined ? res.data[key] : res.data,
  }));
}

/**
 * documentWithLines unwraps a header-plus-lines response — `{"purchase_order": {...},
 * "items": [...]}` — into one object carrying both.
 *
 * Using `field` here instead returns the header alone and silently drops the
 * lines. That disabled four features at once with no error anywhere: the
 * purchase-order detail view, receiving against a PO, creating a GRN from a PO,
 * and the GRN detail view all rendered "no items" for documents that had them.
 * A dropped key is invisible, which is why the unwrapping belongs in one place
 * that knows the shape.
 */
function documentWithLines<T>(request: Promise<AxiosResponse<any>>, key: string): Promise<AxiosResponse<T>> {
  return request.then((res) => ({
    ...res,
    data: { ...(res.data?.[key] ?? res.data), items: res.data?.items ?? [] },
  }));
}

export const storeApi = {
  list: () => list<Store>(api.get('/stores'), 'stores'),
  get: (id: string) => api.get(`/stores/${id}`),
  update: (
    id: string,
    data: Partial<Pick<Store, 'name' | 'address' | 'phone' | 'gstin' | 'location_type'>>
  ) => api.put(`/stores/${id}`, data),
};

export const terminalApi = {
  /**
   * Terminals are new. Each one owns an invoice series, so a sale has to say
   * which counter raised it — the number printed on the bill carries the
   * terminal's code.
   */
  list: (storeId: string) => list<Terminal>(api.get(`/stores/${storeId}/terminals`), 'terminals'),
  create: (storeId: string, data: { code: string; name: string }) =>
    api.post(`/stores/${storeId}/terminals`, data),
  /**
   * Retiring a counter. There is no delete: a terminal's code is embedded in
   * every invoice number it ever issued (INV/T1/2026-27/00001), so the row has
   * to outlive the counter for those bills to remain explicable.
   */
  deactivate: (storeId: string, terminalId: string) =>
    api.post(`/stores/${storeId}/terminals/${terminalId}/deactivate`),
};

export const authApi = {
  login: (username: string, password: string) =>
    api.post('/auth/login', { username, password }),
  logout: () => api.post('/auth/logout'),
  register: (username: string, password: string, role: string, storeId: string) =>
    api.post('/auth/register', { username, password, role, store_id: storeId }),
  listUsers: (storeId: string) => list<any>(api.get('/auth/users', { params: { store_id: storeId } }), 'users'),
  deactivateUser: (id: string) => api.post(`/auth/users/${id}/deactivate`),
  activateUser: (id: string) => api.post(`/auth/users/${id}/activate`),
  changePassword: (oldPassword: string, newPassword: string) =>
    api.post('/auth/change-password', { old_password: oldPassword, new_password: newPassword }),
};

export interface CategoryPayload {
  name: string;
  name_hi?: string;
  name_kn?: string;
  hsn_code?: string;
  /** null puts the category at the top level. */
  parent_id?: string | null;
}

export const categoryApi = {
  list: (storeId: string, includeInactive = false) =>
    list<Category>(
      api.get('/categories', {
        params: { store_id: storeId, ...(includeInactive ? { include_inactive: 'true' } : {}) },
      }),
      'categories'
    ),
  get: (id: string) => api.get<Category>(`/categories/${id}`),
  create: (data: CategoryPayload) => api.post<Category>('/categories', data),
  update: (id: string, data: CategoryPayload) => api.put<Category>(`/categories/${id}`, data),
  /**
   * Folds `id` into `intoId`: products and sub-categories move, then `id` is
   * retired. The direction matters — the first argument is the one that goes
   * away.
   */
  merge: (id: string, intoId: string) =>
    api.post<CategoryMergeResult>(`/categories/${id}/merge`, { into_id: intoId }),
  deactivate: (id: string) => api.post(`/categories/${id}/deactivate`),
  activate: (id: string) => api.post(`/categories/${id}/activate`),
};

export const productApi = {
  list: (params: {
    store_id: string;
    category_id?: string;
    barcode?: string;
    search?: string;
    limit?: number;
    /** Deactivated products too, for the screens that reactivate one. */
    include_inactive?: boolean;
    /**
     * Only products carrying neither an HSN nor a SAC code.
     *
     * New products cannot be saved without one, but a shop that has been
     * trading since before that rule has them on the shelf, and every invoice
     * selling one goes out without the classification Rule 46(g) requires.
     */
    unclassified?: boolean;
  }) => list<Product>(api.get('/products', { params }), 'products'),
  /**
   * Uploads a catalogue spreadsheet.
   *
   * Without `apply` this is a dry run: the server does the whole import and
   * rolls the transaction back, so the report is what would happen rather than
   * a guess. The same call with `apply` commits it.
   */
  import: (
    storeId: string,
    file: File,
    opts: { apply?: boolean; updateExisting?: boolean } = {}
  ) => {
    const body = new FormData();
    body.append('file', file);
    if (opts.apply) body.append('apply', 'true');
    if (opts.updateExisting) body.append('update_existing', 'true');
    // No explicit Content-Type: the browser has to set the multipart boundary
    // itself, and naming the type without one produces a body the server
    // cannot split.
    return api.post<ImportReport>('/products/import', body, { params: { store_id: storeId } });
  },
  /** The empty sheet with the headings that are certain to be accepted. */
  importTemplate: () => api.get('/products/import/template', { responseType: 'blob' }),
  /**
   * The catalogue as a sheet the import reads back.
   *
   * One row per product, carrying its default unit's price. A product also sold
   * by the sack has other prices and those are not here, because an import row
   * can only carry one — and a sheet showing figures an import will ignore is
   * worse than one that does not show them.
   */
  export: (storeId: string, includeInactive = false) =>
    api.get('/products/export', {
      params: { store_id: storeId, ...(includeInactive ? { include_inactive: 'true' } : {}) },
      responseType: 'blob',
    }),
  get: (id: string, storeId: string) => api.get(`/products/${id}`, { params: { store_id: storeId } }),
  /**
   * The units a product may be transacted in: loose by the kilo, a 5 kg bag, a
   * 25 kg sack. `qty_in_base` is how many of the product's own stocking unit
   * one of these amounts to, which is what turns a sale into a stock movement.
   */
  listUnits: (productId: string) => list<any>(api.get(`/products/${productId}/units`), 'units'),
  listPackingUnits: (productId: string) => list<any>(api.get(`/products/${productId}/units`), 'units'),
  createPackingUnit: (
    productId: string,
    data: { label: string; qty_in_parent?: number; barcode?: string; parent_unit_id?: string; sort_order?: number }
  ) => api.post(`/products/${productId}/units`, { ...data, qty_in_base: data.qty_in_parent || 1 }),
  createUnit: (
    productId: string,
    data: {
      label: string;
      qty_in_base: number;
      uom_id?: string;
      barcode?: string;
      is_default?: boolean;
      is_sellable?: boolean;
      is_purchasable?: boolean;
      sort_order?: number;
    }
  ) => api.post(`/products/${productId}/units`, data),
  updateUnit: (productId: string, unitId: string, data: Record<string, unknown>) =>
    api.put(`/products/${productId}/units/${unitId}`, data),
  setDefaultUnit: (productId: string, unitId: string) =>
    api.post(`/products/${productId}/units/${unitId}/default`),
  deactivateUnit: (productId: string, unitId: string) =>
    api.post(`/products/${productId}/units/${unitId}/deactivate`),
  activateUnit: (productId: string, unitId: string) =>
    api.post(`/products/${productId}/units/${unitId}/activate`),
  /** Every unit with its price, including units that have none yet. */
  prices: (productId: string, storeId: string) =>
    list<any>(api.get(`/products/${productId}/prices`, { params: { store_id: storeId } }), 'prices'),
  create: (data: Partial<Product>) => api.post('/products', data),
  update: (id: string, data: Partial<Product>) => api.put(`/products/${id}`, data),
  deactivate: (id: string) => api.post(`/products/${id}/deactivate`),
  activate: (id: string) => api.post(`/products/${id}/activate`),
  /**
   * A price belongs to a unit, not to a product. That is the change that lets a
   * 25 kg sack cost less per kilo than the loose scoop — which is the whole
   * reason a shop sells bulk.
   */
  setPrice: async (data: {
    product_id: string;
    product_unit_id?: string;
    store_id: string;
    mrp: number;
    cost_price: number;
    sell_price: number;
    wholesale_sell_price?: number;
    /** Why it is changing. Recorded beside the figures in the price history. */
    note?: string;
  }) => {
    let unitId = data.product_unit_id;
    if (!unitId) {
      try {
        const res = await productApi.listUnits(data.product_id);
        const units = (res.data as any)?.units || res.data || [];
        unitId = units.find((u: any) => u.is_default)?.id || units[0]?.id;
      } catch {
        /* ignore */
      }
    }
    return api.post('/products/price', { ...data, product_unit_id: unitId || '' });
  },
  /**
   * What this price has been, newest first, each entry carrying the one before
   * it. Without a unit it covers every unit of the product — a product sold
   * loose and by the sack has two series, and reading one alone is how somebody
   * concludes the wrong thing about the other.
   */
  priceHistory: (productId: string, storeId: string, productUnitId?: string) =>
    list<PriceMovement>(
      api.get(`/products/${productId}/price-history`, {
        params: { store_id: storeId, ...(productUnitId ? { product_unit_id: productUnitId } : {}) },
      }),
      'history'
    ),
};

export const priceChangeApi = {
  /**
   * Every price that moved in this store lately — the store-wide counterpart to
   * one product's history. Entries are deliberately not paired: they are from
   * different products, so the row below one of them is not what it used to be.
   */
  list: (storeId: string, days: number) =>
    list<PriceMovement>(
      api.get('/price-changes', { params: { store_id: storeId, days } }),
      'changes'
    ),
};

export const invoiceApi = {
  list: (params: { store_id: string; start_date?: string; end_date?: string; customer_id?: string }) =>
    list<Invoice>(api.get('/invoices', { params }), 'invoices'),
  get: (id: string) => api.get(`/invoices/${id}`),
  /**
   * A sale now carries the terminal that raised it and, optionally, an
   * idempotency key. The key is what makes a retried request safe: the same
   * key returns the original bill rather than raising a second one, which is
   * how a double-tapped Checkout stops producing two invoices.
   */
  create: (data: Partial<Invoice> & { terminal_id: string }, idempotencyKey?: string) =>
    api.post('/invoices', data, {
      headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
    }),
  void: (id: string) => api.post(`/invoices/${id}/void`),
  // Returns the envelope {month, invoices}: the screen reads both.
  exportGSTR1: (storeId: string, month: string) =>
    api.get('/invoices/gstr1', { params: { store_id: storeId, month } }),
  markGSTR1Filed: (storeId: string, month: string) =>
    api.post('/invoices/gstr1/mark-filed', { store_id: storeId, month }),
};

export const customerApi = {
  list: (storeId: string, search?: string) =>
    list<Customer>(api.get('/customers', { params: { store_id: storeId, search: search || undefined } }), 'customers'),
  get: (id: string) => api.get(`/customers/${id}`),
  outstanding: (id: string) => field<number>(api.get(`/customers/${id}/outstanding`), 'outstanding'),
  create: (data: Partial<Customer>) => api.post('/customers', data),
  update: (id: string, data: Partial<Pick<Customer, 'name' | 'name_hi' | 'phone' | 'address' | 'gstin' | 'credit_limit' | 'is_wholesale'>>) =>
    api.put(`/customers/${id}`, data),
};

/**
 * What a reconciliation screen needs: the things a shop has to look at after a
 * till has been offline.
 */
export const reconciliationApi = {
  list: (storeId: string, opts?: { resolved?: boolean; kind?: string }) =>
    list<any>(
      api.get('/reconciliation', {
        params: {
          store_id: storeId,
          resolved: opts?.resolved ? 'true' : undefined,
          kind: opts?.kind || undefined,
        },
      }),
      'exceptions'
    ),
  countOpen: (storeId: string) =>
    field<number>(api.get('/reconciliation/count', { params: { store_id: storeId } }), 'open'),
  resolve: (id: string, storeId: string, note: string) =>
    api.post(`/reconciliation/${id}/resolve`, { note }, { params: { store_id: storeId } }),
};

export const barcodeApi = {
  lookup: (barcode: string, storeId: string) =>
    api.get(`/barcodes/lookup/${encodeURIComponent(barcode)}`, { params: { store_id: storeId } }),

  generate: async (data?: { category_id?: string; product_id?: string; prefix?: string }) => {
    try {
      return await api.post('/barcodes/generate', data || {});
    } catch {
      return { data: { barcode: barcodeApi.suggest() } } as any;
    }
  },

  /**
   * Suggests an in-store barcode for loose goods that arrive without one.
   * The 89 prefix is India's GS1 country code, so a suggestion is not mistaken
   * for a manufacturer's own number. It is only a suggestion: the shopkeeper
   * can overwrite it, and the database's unique index rejects a collision.
   */
  suggest: (): string => {
    let digits = '';
    for (let i = 0; i < 11; i += 1) {
      digits += Math.floor(Math.random() * 10).toString();
    }
    return `89${digits}`;
  },
};

export const vendorApi = {
  /**
   * `include_inactive` is what makes a deactivated vendor reachable again: the
   * listing hides them by default, so without it deactivation is a one-way door
   * from the screen that offers it.
   *
   * `limit` is passed through because the server defaults to 100 and the
   * screens search only what arrived — a silent cap is worse than a stated one.
   */
  list: (params?: string | { search?: string; include_inactive?: boolean; limit?: number }) => {
    const query = typeof params === 'string' ? { search: params } : (params ?? {});
    return list<any>(api.get('/vendors', { params: query }), 'vendors');
  },
  get: (id: string) => api.get(`/vendors/${id}`),
  create: (data: {
    name: string; phone?: string; address?: string; gstin?: string;
    state?: string; credit_days?: number; credit_limit?: number;
  }) => api.post('/vendors', data),
  update: (id: string, data: {
    name?: string; phone?: string; address?: string; gstin?: string;
    state?: string; credit_days?: number; credit_limit?: number;
  }) => api.put(`/vendors/${id}`, data),
  deactivate: (id: string) => api.post(`/vendors/${id}/deactivate`),
};

export const paymentApi = {
  /**
   * The idempotency key is what makes a retried settlement safe. A till syncing
   * a backlog sends each one under a key derived from the sale's, so a reply it
   * never sees is answered with the original settlement rather than taking the
   * money a second time.
   */
  addSplitPayments: (
    data: { invoice_id: string; payments: { payment_mode: string; amount: number; reference?: string }[] },
    idempotencyKey?: string
  ) =>
    api.post('/payments/split', data, {
      headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
    }),
  getInvoicePayments: (invoiceId: string) =>
    list<any>(api.get(`/payments/invoice/${invoiceId}`), 'payments'),
  getSettlement: (params: { store_id: string; date?: string }) =>
    api.get('/payments/settlement', { params }),
};

export const returnApi = {
  list: (storeId: string) => list<any>(api.get('/returns', { params: { store_id: storeId } }), 'returns'),
  /**
   * The idempotency key makes a retried return safe, which is what a till
   * syncing a backlog needs: the same key records one return rather than paying
   * a customer twice on paper.
   */
  create: (
    data: {
      product_id: string;
      quantity: number;
      amount: number;
      reason: string;
      refund_mode?: string;
      store_id: string;
      invoice_id?: string;
      raised_offline?: boolean;
      raised_at?: string;
    },
    idempotencyKey?: string
  ) =>
    api.post('/returns', data, {
      headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
    }),
  approve: (id: string, storeId: string) => api.post(`/returns/${id}/approve`, null, { params: { store_id: storeId } }),
};

export const lossApi = {
  list: (params: { store_id: string; loss_type?: string }) => list<any>(api.get('/losses', { params }), 'losses'),
  create: (data: { product_id: string; store_id: string; batch_no?: string; quantity: number; loss_type: string; reason?: string }) =>
    api.post('/losses', data),
};

export const stockApi = {
  list: (storeId: string) => list<any>(api.get('/stock', { params: { store_id: storeId } }), 'stock'),
  /**
   * The reorder list: everything at or below its level, carrying its supplier,
   * the unit the shop buys it in, what one last cost and how many to order.
   */
  low: (storeId: string) =>
    list<ReorderItem>(api.get('/stock/low', { params: { store_id: storeId } }), 'low_stock'),
  adjustments: (storeId: string) => list<any>(api.get('/stock/adjustments', { params: { store_id: storeId } }), 'adjustments'),
  productStock: (productId: string, storeId: string) =>
    list<any>(api.get(`/stock/product/${productId}`, { params: { store_id: storeId } }), 'batches'),
  getProductStock: (productId: string, storeId: string) =>
    list<any>(api.get(`/stock/product/${productId}`, { params: { store_id: storeId } }), 'batches'),
  /**
   * Move stock in or out.
   *
   * Give EITHER `quantity` in the product's base unit, OR `product_unit_id`
   * with `unit_qty` — "book in 8 sacks" rather than "book in 200". The server
   * multiplies by the unit's size and records what was actually meant, so the
   * history reads "8 x 25 kg sack" instead of a bare 200.
   */
  adjust: (data: {
    product_id: string; store_id: string; batch_no?: string;
    quantity?: number; product_unit_id?: string; unit_qty?: number;
    type: string; reason: string; cost_price?: number; expiry_date?: string; mfg_date?: string;
  }) => api.post('/stock/adjust', data),
  /**
   * Move a batch to another store.
   *
   * The source goes in the query string, NOT the body. The server takes it from
   * the store the middleware already authorised and ignores any store id in the
   * body — deliberately, so a caller cannot name a shop it may not touch. Sent
   * as `from_store_id` in the body it was silently discarded, and the transfer
   * came out of whichever store the user happened to be signed in to while the
   * screen showed them moving another's stock.
   */
  transfer: (data: { product_id: string; from_store_id?: string; to_store_id: string; quantity: number; batch_no?: string; reason?: string }) =>
    api.post(
      '/stock/transfer',
      { product_id: data.product_id, to_store_id: data.to_store_id, quantity: data.quantity, batch_no: data.batch_no, reason: data.reason },
      data.from_store_id ? { params: { store_id: data.from_store_id } } : undefined
    ),
};

// `convert` is deliberately absent. It used to split a batch into another unit,
// which made sense when each pack had its own stock. Stock is now a single pool
// in the base unit, so there is nothing to convert between — and the endpoint
// is gone from the server, so keeping the method here would only aim a button
// at a 404.

export const uomApi = {
  list: (familyId?: string, includeInactive = false) =>
    list<UnitOfMeasure>(
      api.get('/uoms', {
        params: {
          ...(familyId ? { family_id: familyId } : {}),
          ...(includeInactive ? { include_inactive: 'true' } : {}),
        },
      }),
      'units'
    ),
  create: (data: { code: string; name: string; decimal_places?: number; family_id: string; factor_to_base: number }) =>
    api.post('/uoms', data),
  update: (id: string, data: { name?: string; decimal_places?: number; factor_to_base?: number }) =>
    api.put(`/uoms/${id}`, data),
  deactivate: (id: string) => api.post(`/uoms/${id}/deactivate`),
  activate: (id: string) => api.post(`/uoms/${id}/activate`),
};

/**
 * Unit families group the units that can convert between each other — weight,
 * volume, count, length, area, and whatever a trade adds. Units convert only
 * within a family, which is what makes selling rice by the litre impossible
 * rather than merely discouraged.
 */
export const unitFamilyApi = {
  list: (includeInactive = false) =>
    list<UnitFamily>(
      api.get('/unit-families', { params: includeInactive ? { include_inactive: 'true' } : {} }),
      'families'
    ),
  create: (data: { code: string; name: string }) => api.post<UnitFamily>('/unit-families', data),
  update: (id: string, data: { name: string }) => api.put<UnitFamily>(`/unit-families/${id}`, data),
  setBaseUnit: (id: string, uomId: string) => api.post(`/unit-families/${id}/base-unit`, { uom_id: uomId }),
  deactivate: (id: string) => api.post(`/unit-families/${id}/deactivate`),
  activate: (id: string) => api.post(`/unit-families/${id}/activate`),
};

export const analyticsApi = {
  dashboard: (params: { store_id: string; range?: string }) =>
    api.get('/analytics/dashboard', { params }),
  salesByCategory: (params: { store_id: string; start_date: string; end_date: string }) =>
    list<any>(api.get('/analytics/sales/category', { params }), 'categories'),
  topProducts: (params: { store_id: string; start_date: string; end_date: string; limit?: number }) =>
    list<any>(api.get('/analytics/sales/top', { params }), 'products'),
  hourlySales: (params: { store_id: string; date: string }) =>
    list<any>(api.get('/analytics/sales/hourly', { params }), 'hours'),
  paymentBreakup: (params: { store_id: string; start_date: string; end_date: string }) =>
    list<any>(api.get('/analytics/sales/payment', { params }), 'payment_modes'),
  multiStore: (params: { store_ids?: string; range?: string }) =>
    api.get('/analytics/multi-store', { params }),
};

export const purchaseApi = {
  list: (params: { store_id: string; status?: string; vendor_id?: string; limit?: number }) =>
    list<any>(api.get('/purchases', { params }), 'purchase_orders'),
  get: (id: string) => documentWithLines<any>(api.get(`/purchases/${id}`), 'purchase_order'),
  /**
   * An order is placed in the unit the shop actually buys in — ten sacks, not
   * two hundred and fifty kilos — so `unit_qty` counts buying units and `rate`
   * is the price of one of them. `quantity` is the legacy alias for `unit_qty`,
   * kept so an older caller still works.
   *
   * `uom_id` is a unit of MEASURE and is not `product_unit_id`; they are
   * different things and setting one for the other silently prices the line
   * against the wrong pack.
   */
  create: (data: {
    store_id?: string; vendor_id: string; terminal_id?: string; order_date?: string; expected_date?: string;
    items: {
      product_id: string;
      product_unit_id?: string;
      unit_qty?: number;
      quantity?: number;
      uom_id?: string;
      rate: number;
      discount?: number;
      total?: number;
    }[];
  }) => {
    const terminal_id = data.terminal_id || localStorage.getItem('terminal_id') || '00000000-0000-0000-0000-000000000001';
    const items = data.items.map(it => ({
      ...it,
      unit_qty: it.unit_qty ?? it.quantity ?? 1,
      quantity: it.quantity ?? it.unit_qty ?? 1,
    }));
    return api.post('/purchases', { ...data, terminal_id, items });
  },
  /**
   * Receiving. Quantities are counted in `product_unit_id`, which defaults to
   * the unit the order line was placed in, so a clerk confirming ten sacks
   * against the paperwork in their hand means ten sacks. `cost_price` is per
   * buying unit and the server divides it down to the stocking unit.
   */
  receive: (data: {
    po_id: string; store_id: string;
    items: {
      product_id: string;
      product_unit_id?: string;
      received_qty: number;
      rejected_qty?: number;
      accepted_qty?: number;
      reason?: string;
      quantity?: number;
      batch_no?: string;
      cost_price?: number;
      /**
       * What the packet says about this lot, as YYYY-MM-DD.
       *
       * A product that expires needs one of them. Given only `mfg_date`, the
       * server counts the product's own shelf life forward from it — which is
       * what most Indian packaging carries: a date of packing and "best before
       * N days".
       */
      expiry_date?: string;
      mfg_date?: string;
    }[];
  }) => api.post('/purchases/receive', data),
  cancel: (id: string) => api.post(`/purchases/cancel/${id}`),
};

export const expiryApi = {
  expiring: (params: { store_id: string; days?: string }) =>
    list<any>(api.get('/expiry/expiring', { params }), 'batches'),
  expired: (params: { store_id: string }) => list<any>(api.get('/expiry/expired', { params }), 'batches'),
};

export const grnApi = {
  list: (params: { store_id: string; po_id?: string; limit?: number }) =>
    list<any>(api.get('/grn', { params }), 'goods_receipts'),
  get: (id: string) => documentWithLines<any>(api.get(`/grn/${id}`), 'goods_receipt'),
  /** The same operation as purchaseApi.receive, and the same unit rules. */
  create: (data: {
    po_id: string; received_date: string; notes?: string;
    items: {
      product_id: string;
      product_unit_id?: string;
      ordered_qty?: number;
      received_qty: number;
      accepted_qty?: number;
      rejected_qty?: number;
      reason?: string;
      batch_no?: string;
      cost_price?: number;
    }[];
  }) => api.post('/grn', data),
};

export const reportApi = {
  getProfitLoss: (params: { store_id: string; start_date?: string; end_date?: string }) =>
    api.get('/reports/profit-loss', { params }),
  getCreditAging: (params: { store_id: string }) => api.get('/reports/credit-aging', { params }),
  getGSTR3B: (params: { store_id: string; month: string }) => api.get('/reports/gstr-3b', { params }),
};

export const auditApi = {
  list: (params: { store_id: string; user_id?: string; table?: string; limit?: string }) =>
    list<any>(api.get('/audit', { params }), 'entries'),
  // The resources the trail mentions, for the filter to offer. Asking the
  // server keeps the options from drifting out of step with what it records.
  tables: (storeId: string) =>
    list<string>(api.get('/audit/tables', { params: { store_id: storeId } }), 'tables'),
};

export const creditApi = {
  list: (params: { store_id: string; customer_id?: string }) =>
    list<any>(api.get('/credit', { params }), 'entries'),
  overdue: (storeId: string) => list<any>(api.get('/credit/overdue', { params: { store_id: storeId } }), 'customers'),
  payment: (data: { customer_id: string; amount: number; method?: string; notes?: string }) =>
    api.post('/credit/payment', data),
};

export const expenseApi = {
  list: (params: { store_id: string; start_date?: string; end_date?: string; category_id?: string; limit?: number }) =>
    list<any>(api.get('/expenses', { params }), 'expenses'),
  create: (data: { store_id: string; category_id: string; amount: number; description?: string; expense_date: string }) =>
    api.post('/expenses', data),
  getCategories: () => list<any>(api.get('/expenses/categories'), 'categories'),
  /**
   * Without this an expense could not be recorded at all on a fresh install:
   * every expense needs a category, and there was no way to make the first one.
   */
  createCategory: (data: { name: string; description?: string }) =>
    api.post('/expenses/categories', data),
  getSummary: (params: { store_id: string; start_date?: string; end_date?: string }) =>
    api.get('/expenses/summary', { params }),
};

export const vendorPaymentApi = {
  list: (params: { store_id: string; vendor_id?: string; limit?: number }) =>
    list<any>(api.get('/vendor-payments', { params }), 'payments'),
  create: (data: {
    vendor_id: string; po_id?: string; amount: number; payment_mode: string;
    reference?: string; payment_date: string; notes?: string; store_id: string;
  }) => api.post('/vendor-payments', data),
  summary: (params: { store_id: string; start_date?: string; end_date?: string }) =>
    api.get('/vendor-payments/summary', { params }),
};

export const purchaseReturnApi = {
  list: (params: { store_id: string; vendor_id?: string; status?: string; limit?: number }) =>
    list<any>(api.get('/purchase-returns', { params }), 'purchase_returns'),
  get: (id: string, storeId: string) =>
    documentWithLines<any>(api.get(`/purchase-returns/${id}`, { params: { store_id: storeId } }), 'purchase_return'),
  create: (data: {
    po_id?: string; vendor_id: string; store_id: string; return_date: string; reason?: string;
    items: { product_id: string; quantity: number; rate: number }[];
  }) => api.post('/purchase-returns', data),
  approve: (id: string, storeId: string) =>
    api.post(`/purchase-returns/${id}/approve`, null, { params: { store_id: storeId } }),
};

export default api;
