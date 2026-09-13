export function createStorage(values = new Map()) {
    return {
        getItem(key) { return values.get(key) || null; },
        setItem(key, value) { values.set(key, value); },
        removeItem(key) { values.delete(key); },
    };
}

export function installTestWindow({ origin = "http://localhost:8080" } = {}) {
    const previousWindow = globalThis.window;
    globalThis.window = {
        localStorage: createStorage(),
        sessionStorage: createStorage(),
        crypto: { randomUUID: () => "browser-generated-uuid" },
        location: { origin },
    };
    return () => { globalThis.window = previousWindow; };
}

export function createClassList(...initial) {
    const values = new Set(initial);
    return {
        add(...items) { items.forEach(value => values.add(value)); },
        remove(...items) { items.forEach(value => values.delete(value)); },
        contains(value) { return values.has(value); },
    };
}
