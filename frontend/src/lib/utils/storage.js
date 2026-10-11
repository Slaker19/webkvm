// storage.js — safe localStorage wrapper with in-memory fallback.
// Handles environments where window.localStorage is missing, disabled or throws
// (e.g. Node v26+, private browsing mode, restricted iframes, SSR).

class MemoryStorage {
  constructor() {
    this.map = new Map();
  }

  getItem(key) {
    return this.map.has(String(key)) ? this.map.get(String(key)) : null;
  }

  setItem(key, value) {
    this.map.set(String(key), String(value));
  }

  removeItem(key) {
    this.map.delete(String(key));
  }

  clear() {
    this.map.clear();
  }

  get length() {
    return this.map.size;
  }

  key(index) {
    return Array.from(this.map.keys())[index] ?? null;
  }
}

function resolveStorage() {
  if (typeof window !== 'undefined' && window.localStorage) {
    try {
      const testKey = '__storage_test__';
      window.localStorage.setItem(testKey, testKey);
      window.localStorage.removeItem(testKey);
      return window.localStorage;
    } catch {
      // SecurityError or QuotaExceededError in restricted browsers
    }
  }
  return new MemoryStorage();
}

const backend = resolveStorage();

export const safeStorage = {
  getItem(key) {
    try {
      return backend.getItem(key);
    } catch {
      return null;
    }
  },
  setItem(key, value) {
    try {
      backend.setItem(key, value);
    } catch {
      /* ignore quota or access errors */
    }
  },
  removeItem(key) {
    try {
      backend.removeItem(key);
    } catch {
      /* ignore */
    }
  },
  clear() {
    try {
      backend.clear();
    } catch {
      /* ignore */
    }
  },
  get length() {
    try {
      return backend.length;
    } catch {
      return 0;
    }
  },
  key(index) {
    try {
      return backend.key(index);
    } catch {
      return null;
    }
  },
};
