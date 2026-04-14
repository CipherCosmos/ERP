import { useState, useEffect, useCallback } from 'react';
import { useAppStore } from '../stores/appStore';
import { productApi, customerApi, categoryApi } from '../services/api';
import {
  initDB,
  saveProducts,
  getProducts,
  saveCustomers,
  getCustomers,
  saveCategories,
  getCategories,
} from '../services/offlineDb';
import type { Product, Customer, Category } from '../types';

interface OfflineDataState {
  products: Product[];
  customers: Customer[];
  categories: Category[];
  loading: boolean;
  isOfflineData: boolean;
}

/**
 * Hook that fetches products, customers, and categories from the API on mount.
 *
 *  - On success: saves the data into IndexedDB for future offline use.
 *  - On failure (offline / network error): loads previously cached data from IndexedDB.
 *
 * Returns the data along with loading state and a flag indicating whether the
 * data came from the offline cache.
 */
export function useOfflineData(): OfflineDataState {
  const store = useAppStore((s) => s.store);
  const storeId = store?.id ?? '';

  const [products, setProducts] = useState<Product[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [categories, setCategories] = useState<Category[]>([]);
  const [loading, setLoading] = useState(true);
  const [isOfflineData, setIsOfflineData] = useState(false);

  const fetchData = useCallback(async () => {
    if (!storeId) {
      setLoading(false);
      return;
    }

    setLoading(true);

    // Ensure IndexedDB is ready
    try {
      await initDB();
    } catch (err) {
      console.error('[useOfflineData] Failed to initialise IndexedDB:', err);
    }

    try {
      // Fetch all three in parallel from the API
      const [prodRes, custRes, catRes] = await Promise.all([
        productApi.list({ store_id: storeId }),
        customerApi.list(storeId),
        categoryApi.list(storeId),
      ]);

      const prodData: Product[] = prodRes.data ?? [];
      const custData: Customer[] = custRes.data ?? [];
      const catData: Category[] = catRes.data ?? [];

      setProducts(prodData);
      setCustomers(custData);
      setCategories(catData);
      setIsOfflineData(false);

      // Persist to IndexedDB in the background (fire-and-forget)
      saveProducts(prodData).catch((e) =>
        console.warn('[useOfflineData] Failed to cache products:', e)
      );
      saveCustomers(custData).catch((e) =>
        console.warn('[useOfflineData] Failed to cache customers:', e)
      );
      saveCategories(catData).catch((e) =>
        console.warn('[useOfflineData] Failed to cache categories:', e)
      );
    } catch (networkErr) {
      // Network request failed -- fall back to IndexedDB
      console.warn(
        '[useOfflineData] Network unavailable, loading from IndexedDB',
        networkErr
      );

      try {
        const [offlineProds, offlineCusts, offlineCats] = await Promise.all([
          getProducts<Product>(),
          getCustomers<Customer>(),
          getCategories<Category>(),
        ]);

        setProducts(offlineProds);
        setCustomers(offlineCusts);
        setCategories(offlineCats);
        setIsOfflineData(true);
      } catch (dbErr) {
        console.error('[useOfflineData] IndexedDB fallback also failed:', dbErr);
        // Keep state empty -- nothing we can do
        setIsOfflineData(true);
      }
    } finally {
      setLoading(false);
    }
  }, [storeId]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  return { products, customers, categories, loading, isOfflineData };
}
