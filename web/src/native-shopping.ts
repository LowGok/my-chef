import { ShoppingStorageError, type ShoppingStorage } from "./shopping-cache";

export type NativeShoppingSnapshot = {
  version: 1;
  accountId: string;
  accountInstance: string;
  payload: string;
};
export type NativeShoppingBridge = {
  readShopping(): Promise<{
    value: NativeShoppingSnapshot | null;
    generation: string;
  }>;
  writeShopping(input: {
    value: NativeShoppingSnapshot | null;
    generation: string;
  }): Promise<void>;
  clearShopping(): Promise<{ generation: string }>;
};
const prefix = "mychef.shopping.";
const failed = () => new ShoppingStorageError();
const generationValid = (value: unknown): value is string =>
  typeof value === "string" && /^[a-f0-9-]{36}$/.test(value);

function validate(value: NativeShoppingSnapshot) {
  if (
    value.version !== 1 ||
    !/^\d{1,20}$/.test(value.accountId) ||
    !/^[a-f0-9]{32}$/.test(value.accountInstance) ||
    typeof value.payload !== "string" ||
    new TextEncoder().encode(value.payload).length > 8 * 1024 * 1024
  )
    throw failed();
  const payload = JSON.parse(value.payload);
  if (
    payload.version !== 1 ||
    !payload.entries ||
    typeof payload.entries !== "object" ||
    Array.isArray(payload.entries)
  )
    throw failed();
  const entries = new Map<string, string>(Object.entries(payload.entries));
  const owner = JSON.parse(entries.get(prefix + "owner") ?? "null");
  if (
    owner?.profile?.id !== value.accountId ||
    owner.profile.accountInstance !== value.accountInstance
  )
    throw failed();
  for (const [key, raw] of entries) {
    if (typeof raw !== "string") throw failed();
    if (key === prefix + "owner" || key === prefix + "suspended") continue;
    if (
      !key.startsWith(prefix + `list.${value.accountId}.`) ||
      !/^\d{4}-\d{2}-\d{2}$/.test(
        key.slice((prefix + `list.${value.accountId}.`).length),
      )
    )
      throw failed();
    const packet = JSON.parse(raw);
    if (
      packet.privateImports ||
      packet.base?.privateImports ||
      !Array.isArray(packet.base?.items) ||
      !Array.isArray(packet.queue)
    )
      throw failed();
  }
  return entries;
}

class NativeShoppingStorage implements ShoppingStorage {
  #values: Map<string, string>;
  #revision = 0;
  #persisted = 0;
  #epoch = 0;
  #writing: Promise<void> | null = null;
  #writingEpoch = -1;
  #clearing: Promise<void> | null = null;
  #scheduled = false;
  constructor(
    private bridge: NativeShoppingBridge,
    private generation: string,
    value: NativeShoppingSnapshot | null,
  ) {
    this.#values = value ? validate(value) : new Map();
  }
  get length() {
    return this.#values.size;
  }
  key(index: number) {
    return [...this.#values.keys()][index] ?? null;
  }
  getItem(key: string) {
    return this.#values.get(key) ?? null;
  }
  #changed() {
    this.#revision++;
    if (this.#scheduled) return;
    this.#scheduled = true;
    queueMicrotask(() => {
      this.#scheduled = false;
      // Save/sync boundaries await flush and present storage failures. The
      // opportunistic metadata write must not create an unhandled rejection.
      void this.flush().catch(() => undefined);
    });
  }
  setItem(key: string, value: string) {
    if (!key.startsWith(prefix) || typeof value !== "string") throw failed();
    this.#values.set(key, value);
    this.#changed();
  }
  removeItem(key: string) {
    if (this.#values.delete(key)) this.#changed();
  }
  #snapshot(): NativeShoppingSnapshot | null {
    const owner = JSON.parse(this.getItem(prefix + "owner") ?? "null");
    if (!owner || this.getItem(prefix + "signed-out") === "1") return null;
    const entries: Record<string, string> = {};
    for (const [key, value] of this.#values) {
      if (
        key === prefix + "owner" ||
        key === prefix + "suspended" ||
        key.startsWith(prefix + `list.${owner.profile.id}.`)
      )
        entries[key] = value;
    }
    const snapshot: NativeShoppingSnapshot = {
      version: 1,
      accountId: owner.profile.id,
      accountInstance: owner.profile.accountInstance,
      payload: JSON.stringify({ version: 1, entries }),
    };
    validate(snapshot);
    return snapshot;
  }
  async flush(): Promise<void> {
    await this.#clearing;
    if (this.#writing) {
      const writingEpoch = this.#writingEpoch;
      try {
        await this.#writing;
      } catch (error) {
        if (writingEpoch === this.#epoch) throw error;
      }
      if (this.#persisted !== this.#revision) return this.flush();
      return;
    }
    if (this.#persisted === this.#revision) return;
    const revision = this.#revision,
      epoch = this.#epoch;
    let snapshot: NativeShoppingSnapshot | null;
    try {
      snapshot = this.#snapshot();
    } catch {
      throw failed();
    }
    const writing = this.bridge
      .writeShopping({ value: snapshot, generation: this.generation })
      .then(() => {
        if (epoch === this.#epoch) this.#persisted = revision;
      })
      .catch(() => {
        throw failed();
      });
    this.#writing = writing;
    this.#writingEpoch = epoch;
    try {
      await writing;
    } finally {
      if (this.#writing === writing) this.#writing = null;
    }
    if (epoch === this.#epoch && this.#persisted !== this.#revision)
      await this.flush();
  }
  erase(): Promise<void> {
    this.#values.clear();
    this.#epoch++;
    this.#revision++;
    if (this.#clearing) return this.#clearing;
    const revision = this.#revision;
    // Send erasure immediately. Native generations reject older writes even
    // when their replies arrive after the account has already changed.
    const clearing = this.bridge
      .clearShopping()
      .then(({ generation }) => {
        if (!generationValid(generation)) throw failed();
        this.generation = generation;
        this.#persisted = revision;
      })
      .catch(() => {
        throw failed();
      });
    this.#clearing = clearing;
    void clearing.catch(() => undefined);
    return clearing.finally(() => {
      if (this.#clearing === clearing) this.#clearing = null;
    });
  }
}

export async function openNativeShoppingStorage(
  bridge: NativeShoppingBridge,
): Promise<ShoppingStorage> {
  try {
    const { value, generation } = await bridge.readShopping();
    if (!generationValid(generation)) throw failed();
    return new NativeShoppingStorage(bridge, generation, value);
  } catch {
    throw failed();
  }
}
