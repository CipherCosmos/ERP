export type UserRole = 'owner' | 'manager' | 'cashier' | 'helper' | 'warehouse';

export interface User {
  id: string;
  username: string;
  role: UserRole;
  store_id: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface Store {
  id: string;
  name: string;
  address: string;
  phone: string;
  gstin: string;
  /** 'store' = retail outlet, 'warehouse' = godown / central stock */
  location_type?: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface Category {
  id: string;
  name: string;
  name_hi: string;
  name_kn: string;
  parent_id: string | null;
  hsn_code: string;
  is_active: boolean;
}

export interface Product {
  id: string;
  name: string;
  name_hi?: string;
  name_kn?: string;
  category_id: string;
  hsn_code: string;
  barcode: string;
  uom_id: string;
  base_unit_qty: number;
  gst_rate: number;
  is_free: boolean;
  is_active: boolean;
  allow_decimal: boolean;
  min_stock: number;
  sell_price: number;
  mrp: number;
  wholesale_sell_price?: number;
}

export interface ProductPrice {
  id: string;
  product_id: string;
  store_id: string;
  mrp: number;
  cost_price: number;
  sell_price: number;
  is_active: boolean;
}

export interface Stock {
  id: string;
  product_id: string;
  store_id: string;
  batch_no: string;
  quantity: number;
  reserved_qty: number;
  expiry_date: string | null;
  cost_price: number;
}

export interface Customer {
  id: string;
  name: string;
  name_hi?: string;
  phone: string;
  address: string;
  gstin: string;
  credit_limit: number;
  /** When true, POS prefers wholesale_sell_price when set */
  is_wholesale?: boolean;
  store_id?: string;
  is_active: boolean;
}

export interface Invoice {
  id: string;
  invoice_no: string;
  store_id: string;
  user_id: string;
  customer_id: string | null;
  invoice_date: string;
  sub_total: number;
  cgst: number;
  sgst: number;
  igst: number;
  discount: number;
  round_off: number;
  total: number;
  status: string;
  payment_mode: string;
  is_gstr1_filed: boolean;
}

export interface InvoiceItem {
  id: string;
  invoice_id: string;
  product_id: string;
  batch_no: string;
  quantity: number;
  rate: number;
  discount: number;
  cgst: number;
  sgst: number;
  igst: number;
  total: number;
  unit_cost_at_sale?: number;
}

export interface CartItem {
  product: Product;
  quantity: number;
  batch_no?: string;
  discount: number;
}

export interface CreditEntry {
  id: string;
  customer_id: string;
  invoice_id: string | null;
  amount: number;
  type: 'issue' | 'payment';
  due_date: string | null;
  paid_amount: number;
  status: 'pending' | 'partial' | 'paid';
}

export interface Vendor {
  id: string;
  name: string;
  phone: string;
  address: string;
  gstin: string;
  state: string;
  is_active: boolean;
}

export interface PurchaseOrder {
  id: string;
  po_no: string;
  store_id: string;
  vendor_id: string;
  order_date: string;
  expected_date: string | null;
  sub_total: number;
  total: number;
  status: 'pending' | 'received' | 'partial';
}

export interface SyncRecord {
  id: string;
  table_name: string;
  record_id: string;
  operation: 'insert' | 'update' | 'delete';
  store_id: string;
  status: 'pending' | 'synced' | 'failed';
  created_at: string;
  synced_at: string | null;
}