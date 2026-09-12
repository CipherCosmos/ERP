import React, { useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { useAppStore } from './stores/appStore';
import { startTill } from './services/offline/service';
import { storeApi } from './services/api';
import Login from './pages/Login';
import Billing from './pages/Billing';
import Inventory from './pages/Inventory';
import Products from './pages/Products';
import Categories from './pages/Categories';
import ProductImport from './pages/ProductImport';
import PriceChanges from './pages/PriceChanges';
import Dashboard from './pages/Dashboard';
import Menu from './pages/Menu';
import Customers from './pages/Customers';
import Credit from './pages/Credit';
import Vendors from './pages/Vendors';
import Purchases from './pages/Purchases';
import Reports from './pages/Reports';
import GSTR1Reports from './pages/GSTR1';
import Settings from './pages/Settings';
import Expiry from './pages/Expiry';
import Settlement from './pages/Settlement';
import Returns from './pages/Returns';
import Losses from './pages/Losses';
import ProfitLoss from './pages/ProfitLoss';
import CreditAging from './pages/CreditAging';
import Expenses from './pages/Expenses';
import Invoices from './pages/Invoices';
import AuditLog from './pages/AuditLog';
import StockTransfer from './pages/StockTransfer';
import GRN from './pages/GRN';
import VendorPayments from './pages/VendorPayments';
import PurchaseReturns from './pages/PurchaseReturns';
import MultiStoreDashboard from './pages/MultiStoreDashboard';

/**
 * Re-read the signed-in store from the server once per app load.
 *
 * The store record is persisted in localStorage so a reload does not lose the
 * session, but that copy then never changed. A shop that entered its GSTIN in
 * Settings on the office machine went on printing GSTIN-less tax invoices at
 * every till until somebody happened to sign out and in again — and the GSTIN
 * is statutory content on the bill, so those were not documents the shop could
 * legally issue. The same staleness reached the store name and address printed
 * on every receipt.
 *
 * A failure here is deliberately ignored: the cached copy is what the counter
 * had a moment ago, and an offline till must keep billing.
 */
function useFreshStore() {
  const store = useAppStore((s) => s.store);
  const setStore = useAppStore((s) => s.setStore);
  const storeId = store?.id;
  useEffect(() => {
    if (!storeId) return;
    let cancelled = false;
    storeApi
      .get(storeId)
      .then(({ data }) => {
        if (!cancelled && data?.id) setStore(data);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [storeId, setStore]);
}

/**
 * Starts the till for the signed-in shop: load the snapshot it bills from, keep
 * it current while there is a connection, and send whatever is queued.
 *
 * It lives here rather than on the counter screen because the sync loop has to
 * outlive any one screen. A cashier who bills offline and then walks to the
 * stock screen has sales waiting, and those go when the line comes back whether
 * or not anybody is looking at the till.
 */
function useTill() {
  const storeId = useAppStore((s) => s.store?.id);
  const user = useAppStore((s) => s.user);
  useEffect(() => {
    // A persisted user and store are not a session: the token expires and the
    // two outlive it. Without the token check the till starts on a dead session
    // and retries against a server that can only say 401.
    if (!storeId || !user || !localStorage.getItem('token')) return undefined;
    return startTill(storeId);
  }, [storeId, user]);
}

function App() {
  useFreshStore();
  useTill();
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<Menu />} />
        <Route path="/billing" element={<Billing />} />
        <Route path="/inventory" element={<Inventory />} />
        <Route path="/products" element={<Products />} />
        <Route path="/categories" element={<Categories />} />
        <Route path="/products/import" element={<ProductImport />} />
        <Route path="/price-changes" element={<PriceChanges />} />
        <Route path="/dashboard" element={<Dashboard />} />
        <Route path="/customers" element={<Customers />} />
        <Route path="/credit" element={<Credit />} />
        <Route path="/vendors" element={<Vendors />} />
        <Route path="/purchases" element={<Purchases />} />
        <Route path="/reports" element={<Reports />} />
        <Route path="/gstr1" element={<GSTR1Reports />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/expiry" element={<Expiry />} />
        <Route path="/settlement" element={<Settlement />} />
        <Route path="/returns" element={<Returns />} />
        <Route path="/losses" element={<Losses />} />
        <Route path="/profit-loss" element={<ProfitLoss />} />
        <Route path="/credit-aging" element={<CreditAging />} />
        <Route path="/expenses" element={<Expenses />} />
        <Route path="/invoices" element={<Invoices />} />
        <Route path="/audit" element={<AuditLog />} />
        <Route path="/stock-transfer" element={<StockTransfer />} />
        <Route path="/grn" element={<GRN />} />
        <Route path="/vendor-payments" element={<VendorPayments />} />
        <Route path="/purchase-returns" element={<PurchaseReturns />} />
        <Route path="/multi-store" element={<MultiStoreDashboard />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}

export default App;