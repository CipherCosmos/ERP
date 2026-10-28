import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { User, Store, Category, Product, CartItem, Terminal } from '../types';

interface AppState {
  user: User | null;
  /**
   * What this session may do, as the server reports it at sign-in.
   *
   * The menu used to carry its own role list per screen, and the two had drifted
   * in both directions: a warehouse hand who may write off damage had no entry
   * for it, and a helper whose job is reading the reports could not reach them.
   * The server is the only place that decides, so it is the only place that
   * should say. Hiding a control is a courtesy; the API refuses regardless.
   */
  permissions: string[];
  store: Store | null;
  /**
   * The counter this device bills from. Every invoice carries it, because each
   * terminal owns its own number series — the code appears in the bill number,
   * e.g. INV/T1/2026-27/00001.
   */
  terminal: Terminal | null;
  categories: Category[];
  products: Product[];
  cart: CartItem[];
  isOnline: boolean;
  syncPending: number;
  language: string;

  setUser: (user: User | null) => void;
  setPermissions: (permissions: string[]) => void;
  /** True when the session holds the named permission. */
  can: (permission: string) => boolean;
  setStore: (store: Store | null) => void;
  setTerminal: (terminal: Terminal | null) => void;
  setCategories: (categories: Category[]) => void;
  setProducts: (products: Product[]) => void;
  addToCart: (item: CartItem) => void;
  /** lineKey is `${productId}::${unitId}` — see cartLineKey in types. */
  updateCartQuantity: (lineKey: string, quantity: number) => void;
  /** An absolute rupee discount on one line, not a percentage — what the
   *  customer was actually given is then recorded exactly as it was given. */
  updateCartDiscount: (lineKey: string, discount: number) => void;
  removeFromCart: (lineKey: string) => void;
  clearCart: () => void;
  setOnline: (isOnline: boolean) => void;
  setSyncPending: (count: number) => void;
  setLanguage: (language: string) => void;
}

export const useAppStore = create<AppState>()(
  persist(
    (set, get) => ({
      user: null,
      permissions: [],
      store: null,
      terminal: null,
      categories: [],
      products: [],
      cart: [],
      isOnline: navigator.onLine,
      syncPending: 0,
      language: 'en',

      setUser: (user) => set({ user }),
      setPermissions: (permissions) => set({ permissions }),
      can: (permission) => get().permissions.includes(permission),
      setStore: (store) => set({ store }),
      setTerminal: (terminal) => set({ terminal }),
      setCategories: (categories) => set({ categories }),
      setProducts: (products) => set({ products }),

      // A line is identified by product AND unit. Keying on the product alone
      // merges two sacks with 250 g of the same rice into one line, and one of
      // the two prices then silently wins.
      addToCart: (item) => {
        const cart = get().cart;
        const key = (c: CartItem) => `${c.product.id}::${c.unit?.id ?? ''}`;
        const incoming = `${item.product.id}::${item.unit?.id ?? ''}`;
        const existing = cart.find((c) => key(c) === incoming);
        if (existing) {
          set({
            cart: cart.map((c) =>
              key(c) === incoming ? { ...c, quantity: c.quantity + item.quantity } : c
            ),
          });
        } else {
          set({ cart: [...cart, item] });
        }
      },

      updateCartQuantity: (lineKey, quantity) => {
        const cart = get().cart;
        const key = (c: CartItem) => `${c.product.id}::${c.unit?.id ?? ''}`;
        if (quantity <= 0) {
          set({ cart: cart.filter((c) => key(c) !== lineKey) });
        } else {
          set({ cart: cart.map((c) => (key(c) === lineKey ? { ...c, quantity } : c)) });
        }
      },

      updateCartDiscount: (lineKey, discount) => {
        const key = (c: CartItem) => `${c.product.id}::${c.unit?.id ?? ''}`;
        set({
          cart: get().cart.map((c) =>
            key(c) === lineKey ? { ...c, discount: Math.max(0, discount) } : c
          ),
        });
      },

      removeFromCart: (lineKey) => {
        const key = (c: CartItem) => `${c.product.id}::${c.unit?.id ?? ''}`;
        set({ cart: get().cart.filter((c) => key(c) !== lineKey) });
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
        permissions: state.permissions,
        store: state.store,
        terminal: state.terminal,
        language: state.language,
        cart: state.cart,
      }),
      /**
       * The cart lives in localStorage so a counter survives a page reload
       * mid-sale. That also means a browser can rehydrate a cart saved by an
       * older build.
       *
       * Version 2 added the unit to every line. A line saved before it has no
       * unit, so it has no price and nothing to convert into a stock movement —
       * it would sit in the cart looking normal and bill for the wrong amount.
       * Dropping those lines is the honest outcome: the cashier re-scans a few
       * items, rather than the customer being charged from a shape the app no
       * longer understands.
       */
      version: 2,
      migrate: (persisted: any, from: number) => {
        if (!persisted || typeof persisted !== 'object') {
          return {
            user: null,
            permissions: [],
            store: null,
            terminal: null,
            language: 'en',
            cart: [],
          };
        }
        if (from < 2 && persisted.cart) {
          persisted.cart = [];
        }
        return persisted;
      },
    }
  )
);

if (typeof window !== 'undefined') {
  window.addEventListener('online', () => useAppStore.getState().setOnline(true));
  window.addEventListener('offline', () => useAppStore.getState().setOnline(false));
}