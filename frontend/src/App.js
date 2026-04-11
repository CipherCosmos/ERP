import React from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import Login from './pages/Login';
import Billing from './pages/Billing';
import Inventory from './pages/Inventory';
import Products from './pages/Products';
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

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<Menu />} />
        <Route path="/billing" element={<Billing />} />
        <Route path="/inventory" element={<Inventory />} />
        <Route path="/products" element={<Products />} />
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
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}

export default App;