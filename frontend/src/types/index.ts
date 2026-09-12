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

export interface Terminal {
  id: string;
  store_id: string;
  /** Code appears in every invoice number this counter issues, e.g. INV/T1/2026-27/00001 */
  code: string;
  name: string;
  is_active: boolean;
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
  sac_code?: string;
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
  image_url?: string;
  brand?: string;
  description?: string;
}

export interface ProductPrice {
  id: string;
  product_id: string;
  store_id: string;
  mrp: number;
  cost_price: number;
  sell_price: number;
  wholesale_sell_price?: number;
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

export interface StockAdjustment {
  id: string;
  product_id: string;
  store_id: string;
  batch_no?: string;
  quantity_before: number;
  quantity_after: number;
  type: 'add' | 'reduce' | 'conversion' | 'transfer' | 'damage' | 'loss';
  reason?: string;
  cost_price?: number;
  user_id: string;
  created_at: string;
}

export interface PackingUnit {
  id: string;
  product_id: string;
  label: string;
  qty_in_parent?: number;
  barcode?: string;
  parent_unit_id?: string;
  sort_order?: number;
  is_active: boolean;
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

/**
 * A unit a product may be sold in: loose by the kilo, a 5 kg bag, a 25 kg sack.
 * `qty_in_base` is how many of the product's stocking unit one amounts to, and
 * is what turns a sale into a stock movement.
 */
export interface SaleUnit {
  id: string;
  label: string;
  qty_in_base: number;
  sell_price: number;
  has_price?: boolean;
  barcode?: string;
}

export interface CartItem {
  product: Product;
  /**
   * The unit this line is being sold in. A cart line is identified by product
   * AND unit, never by product alone: two sacks and 250 g of the same rice are
   * two lines at two prices, and merging them on product id would silently
   * price one of them wrong.
   */
  unit: SaleUnit;
  /** How many of `unit` — 2 sacks, not 50 kg. Base quantity is derived. */
  quantity: number;
  batch_no?: string;
  discount: number;
}

/** The key that identifies a cart line. */
export function cartLineKey(productId: string, unitId: string): string {
  return `${productId}::${unitId}`;
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

export interface Return {
  id: string;
  product_id: string;
  store_id: string;
  invoice_id?: string;
  quantity: number;
  amount: number;
  reason: string;
  refund_mode?: string;
  status: 'pending' | 'approved';
  user_id: string;
  created_at: string;
}

export interface Loss {
  id: string;
  product_id: string;
  store_id: string;
  batch_no?: string;
  quantity: number;
  loss_type: string;
  reason?: string;
  user_id: string;
  created_at: string;
}

export interface Vendor {
  id: string;
  name: string;
  phone: string;
  address: string;
  gstin: string;
  state: string;
  credit_days?: number;
  credit_limit?: number;
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
  cgst?: number;
  sgst?: number;
  igst?: number;
  total: number;
  status: 'pending' | 'received' | 'partial' | 'cancelled';
}

export interface PurchaseOrderItem {
  id: string;
  po_id: string;
  product_id: string;
  quantity: number;
  uom_id?: string;
  rate: number;
  discount?: number;
  total?: number;
}

export interface VendorPayment {
  id: string;
  vendor_id: string;
  vendor_name?: string;
  po_id?: string;
  amount: number;
  payment_mode: string;
  reference?: string;
  payment_date: string;
  notes?: string;
  user_id: string;
  store_id: string;
  created_at: string;
}

export interface PurchaseReturn {
  id: string;
  po_id?: string;
  vendor_id: string;
  vendor_name?: string;
  store_id: string;
  return_date: string;
  total: number;
  status: 'pending' | 'approved';
  reason?: string;
  user_id: string;
  created_at: string;
}

export interface PurchaseReturnItem {
  id: string;
  return_id: string;
  product_id: string;
  quantity: number;
  rate: number;
  total: number;
}

export interface Expense {
  id: string;
  store_id: string;
  category_id: string;
  amount: number;
  description?: string;
  expense_date: string;
  user_id: string;
  created_at: string;
}

export interface ExpenseCategory {
  id: string;
  name: string;
  description?: string;
  is_active: boolean;
}

export interface AuditLog {
  id: string;
  store_id: string;
  user_id: string;
  table_name: string;
  operation: 'INSERT' | 'UPDATE' | 'DELETE';
  record_id: string;
  changes?: Record<string, unknown>;
  created_at: string;
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