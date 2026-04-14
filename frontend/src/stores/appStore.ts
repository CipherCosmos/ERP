import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { User, Store, Category, Product, CartItem } from '../types';

interface AppState {
  user: User | null;
  store: Store | null;
  categories: Category[];
  products: Product[];
  cart: CartItem[];
  isOnline: boolean;
  syncPending: number;
  language: string;

  setUser: (user: User | null) => void;
  setStore: (store: Store | null) => void;
  setCategories: (categories: Category[]) => void;
  setProducts: (products: Product[]) => void;
  addToCart: (item: CartItem) => void;
  updateCartQuantity: (productId: string, quantity: number) => void;
  removeFromCart: (productId: string) => void;
  clearCart: () => void;
  setOnline: (isOnline: boolean) => void;
  setSyncPending: (count: number) => void;
  setLanguage: (language: string) => void;
}

export const useAppStore = create<AppState>()(
  persist(
    (set, get) => ({
      user: null,
      store: null,
      categories: [],
      products: [],
      cart: [],
      isOnline: navigator.onLine,
      syncPending: 0,
      language: 'en',

      setUser: (user) => set({ user }),
      setStore: (store) => set({ store }),
      setCategories: (categories) => set({ categories }),
      setProducts: (products) => set({ products }),

      addToCart: (item) => {
        const cart = get().cart;
        const existing = cart.find((c) => c.product.id === item.product.id);
        if (existing) {
          set({
            cart: cart.map((c) =>
              c.product.id === item.product.id
                ? { ...c, quantity: c.quantity + item.quantity }
                : c
            ),
          });
        } else {
          set({ cart: [...cart, item] });
        }
      },

      updateCartQuantity: (productId, quantity) => {
        const cart = get().cart;
        if (quantity <= 0) {
          set({ cart: cart.filter((c) => c.product.id !== productId) });
        } else {
          set({
            cart: cart.map((c) =>
              c.product.id === productId ? { ...c, quantity } : c
            ),
          });
        }
      },

      removeFromCart: (productId) => {
        set({ cart: get().cart.filter((c) => c.product.id !== productId) });
      },

      clearCart: () => set({ cart: [] }),

      setOnline: (isOnline) => set({ isOnline }),

      setSyncPending: (syncPending) => set({ syncPending }),

      setLanguage: (language) => set({ language }),
    }),
    {
      name: 'gbos-store',
      partialize: (state) => ({
        user: state.user,
        store: state.store,
        language: state.language,
        cart: state.cart,
      }),
    }
  )
);

if (typeof window !== 'undefined') {
  window.addEventListener('online', () => useAppStore.getState().setOnline(true));
  window.addEventListener('offline', () => useAppStore.getState().setOnline(false));
}