import React, { useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';
import { AppLayout } from '../components/Layout/AppLayout';
import {
  IconDashboard,
  IconPOS,
  IconProducts,
  IconInventory,
  IconWarehouse,
  IconPurchases,
  IconCustomers,
  IconVendors,
  IconCredit,
  IconReturns,
  IconExpenses,
  IconReports,
  IconSettings,
  IconStore,
  IconArrowRight,
} from '../components/Icons';

export default function Menu() {
  const navigate = useNavigate();
  const { user, store } = useAppStore();

  useEffect(() => {
    if (!user) {
      navigate('/login');
      return;
    }
    // Cashiers directly open POS
    if (user.role === 'cashier') {
      navigate('/billing');
    } else {
      navigate('/dashboard');
    }
  }, [user, navigate]);

  return (
    <AppLayout>
      <div style={{ textAlign: 'center', padding: '4rem 1rem', color: 'var(--text-muted)' }}>
        Loading GBOS ERP Operations...
      </div>
    </AppLayout>
  );
}
