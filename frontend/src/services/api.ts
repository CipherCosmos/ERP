import axios from 'axios';
import { Product, Store, Customer, Invoice } from '../types';

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

api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token');
      localStorage.removeItem('storeId');
      window.location.href = '/login';
    }
    return Promise.reject(error);
  }
);

export const storeApi = {
  list: () => api.get('/stores'),
  get: (id: string) => api.get(`/stores/${id}`),
  update: (
    id: string,
    data: Partial<Pick<Store, 'name' | 'address' | 'phone' | 'gstin' | 'location_type'>>
  ) => api.put(`/stores/${id}`, data),
};

export const authApi = {
  login: (username: string, password: string) =>
    api.post('/auth/login', { username, password }),
  register: (username: string, password: string, role: string, storeId: string) =>
    api.post('/auth/register', { username, password, role, store_id: storeId }),
};

export const categoryApi = {
  list: (storeId: string) => api.get(`/categories?store_id=${storeId}`),
  create: (data: { name: string; name_hi?: string; name_kn?: string; hsn_code?: string }) =>
    api.post('/categories', data),
};

export const productApi = {
  list: (params: { store_id: string; category_id?: string; barcode?: string; search?: string }) =>
    api.get('/products', { params }),
  get: (id: string, storeId: string) => api.get(`/products/${id}?store_id=${storeId}`),
  listPackingUnits: (productId: string) => api.get(`/products/${productId}/packing-units`),
  createPackingUnit: (
    productId: string,
    data: { label: string; qty_in_parent?: number; barcode?: string; parent_unit_id?: string; sort_order?: number }
  ) => api.post(`/products/${productId}/packing-units`, data),
  create: (data: Partial<Product>) =>
    api.post('/products', data),
  setPrice: (data: { product_id: string; store_id: string; mrp: number; cost_price: number; sell_price: number; wholesale_sell_price?: number }) =>
    api.post('/products/price', data),
};

export const invoiceApi = {
  list: (params: { store_id: string; start_date?: string; end_date?: string; customer_id?: string }) =>
    api.get('/invoices', { params }),
  get: (id: string) => api.get(`/invoices/${id}`),
  create: (data: Partial<Invoice>) =>
    api.post('/invoices', data),
  exportGSTR1: (storeId: string, month: string) =>
    api.get(`/invoices/gstr1?store_id=${storeId}&month=${month}`),
};

export const customerApi = {
  list: (storeId: string, search?: string) =>
    api.get(`/customers?store_id=${storeId}&search=${search || ''}`),
  create: (data: Partial<Customer>) =>
    api.post('/customers', data),
  update: (id: string, data: Partial<Pick<Customer, 'name' | 'name_hi' | 'phone' | 'address' | 'gstin' | 'credit_limit' | 'is_wholesale'>>) =>
    api.put(`/customers/${id}`, data),
};

export const barcodeApi = {
  lookup: (barcode: string, storeId: string) =>
    api.get(`/barcodes/lookup/${encodeURIComponent(barcode)}`, { params: { store_id: storeId } }),
  generate: (data: { category_id?: string; product_id?: string; prefix?: string }) =>
    api.post('/barcodes/generate', data),
};

export const vendorApi = {
  list: (search?: string) => api.get('/vendors', { params: search ? { search } : {} }),
  get: (id: string) => api.get(`/vendors/${id}`),
  create: (data: {
    name: string;
    phone?: string;
    address?: string;
    gstin?: string;
    state?: string;
    credit_days?: number;
    credit_limit?: number;
  }) => api.post('/vendors', data),
};

export const paymentApi = {
  addSplitPayments: (data: { invoice_id: string; payments: { payment_mode: string; amount: number; reference?: string }[] }) =>
    api.post('/payments/split', data),
  getInvoicePayments: (invoiceId: string) =>
    api.get(`/payments/invoice/${invoiceId}`),
  getSettlement: (params: { store_id: string; date?: string }) =>
    api.get('/payments/settlement', { params }),
};

export const returnApi = {
  list: (storeId: string) => api.get(`/returns?store_id=${storeId}`),
  create: (data: { product_id: string; quantity: number; amount: number; reason: string; refund_mode?: string; store_id: string; user_id: string; invoice_id?: string }) =>
    api.post('/returns', data),
  approve: (id: string, storeId: string) => api.post(`/returns/${id}/approve?store_id=${storeId}`),
};

export const lossApi = {
  list: (params: { store_id: string; loss_type?: string }) => api.get('/losses', { params }),
  create: (data: { product_id: string; store_id: string; batch_no?: string; quantity: number; loss_type: string; reason?: string; user_id: string }) =>
    api.post('/losses', data),
};

export const grnApi = {
  list: (poId?: string) => api.get(`/grn?po_id=${poId || ''}`),
  get: (id: string) => api.get(`/grn/${id}`),
  create: (data: { po_id: string; received_date: string; received_by: string; notes?: string; items: { product_id: string; ordered_qty?: number; received_qty: number; accepted_qty?: number; rejected_qty?: number; reason?: string }[] }) =>
    api.post('/grn', data),
};

export const reportApi = {
  getProfitLoss: (params: { store_id: string; start_date?: string; end_date?: string }) =>
    api.get('/reports/profit-loss', { params }),
  getCreditAging: (params: { store_id: string }) =>
    api.get('/reports/credit-aging', { params }),
  getGSTR3B: (params: { store_id: string; month: string }) =>
    api.get('/reports/gstr-3b', { params }),
};

export const expenseApi = {
  list: (params: { store_id: string; start_date?: string; end_date?: string; category_id?: string }) =>
    api.get('/expenses', { params }),
  create: (data: { store_id: string; category_id: string; amount: number; description?: string; expense_date: string; user_id: string }) =>
    api.post('/expenses', data),
  getCategories: () => api.get('/expenses/categories'),
  getSummary: (params: { store_id: string; start_date?: string; end_date?: string }) =>
    api.get('/expenses/summary', { params }),
};

export default api;