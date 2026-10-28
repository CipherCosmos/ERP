import React from 'react';
import { Navigate } from 'react-router-dom';
import { useAppStore } from '../stores/appStore';

export default function Menu() {
  const user = useAppStore((s) => s.user);
  const token = typeof window !== 'undefined' ? localStorage.getItem('token') : null;

  if (!user && !token) {
    return <Navigate to="/login" replace />;
  }

  const target = user?.role === 'cashier' ? '/billing' : '/dashboard';
  return <Navigate to={target} replace />;
}
