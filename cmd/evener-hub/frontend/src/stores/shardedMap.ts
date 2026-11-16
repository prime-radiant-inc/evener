/**
 * A persistent string-keyed map with bounded copy-on-write. A write copies the
 * touched shard and the shard pointer array, never the whole entry set, so one
 * changed entry costs SHARD_COUNT pointer slots plus one shard's entries
 * instead of every entry in the map.
 *
 * Readers get ordinary ReadonlyMap semantics. The one difference is iteration
 * order: entries iterate shard by shard rather than in insertion order, so this
 * map suits a key-addressed store whose consumers look entries up by key, not
 * one whose consumers read positions.
 */

// A power of two so the shard index is a mask. 32 holds the per-write pointer
// copy to one short array while keeping a store of tens to hundreds of entries
// at a handful of entries per shard.
const SHARD_COUNT = 32;
const SHARD_MASK = SHARD_COUNT - 1;

// FNV-1a over the whole key. Uniformity is what matters: a shard that collects
// a large share of the keys returns the per-write cost toward a whole-map
// copy. Ref keys are short, so the hash is a few dozen nanoseconds.
function shardOf(key: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < key.length; index += 1) {
    hash ^= key.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0) & SHARD_MASK;
}

const NO_SHARDS: ReadonlyArray<undefined> = Object.freeze(new Array<undefined>(SHARD_COUNT).fill(undefined));

type Shards<V> = ReadonlyArray<ReadonlyMap<string, V> | undefined>;

export class ShardedMap<V> implements ReadonlyMap<string, V> {
  private readonly shards: Shards<V>;
  private readonly entryCount: number;

  private constructor(shards: Shards<V>, entryCount: number) {
    this.shards = shards;
    this.entryCount = entryCount;
  }

  static empty<V>(): ShardedMap<V> {
    return new ShardedMap<V>(NO_SHARDS, 0);
  }

  /** Distributes `entries` into shards, one pass. A repeated key keeps its last value, like Map. */
  static from<V>(entries: Iterable<readonly [string, V]>): ShardedMap<V> {
    const shards: Array<Map<string, V> | undefined> = NO_SHARDS.slice();
    for (const [key, value] of entries) {
      const index = shardOf(key);
      let shard = shards[index];
      if (shard === undefined) {
        shard = new Map();
        shards[index] = shard;
      }
      shard.set(key, value);
    }
    let entryCount = 0;
    for (const shard of shards) if (shard !== undefined) entryCount += shard.size;
    return new ShardedMap<V>(shards, entryCount);
  }

  get size(): number {
    return this.entryCount;
  }

  get(key: string): V | undefined {
    return this.shards[shardOf(key)]?.get(key);
  }

  has(key: string): boolean {
    return this.shards[shardOf(key)]?.has(key) === true;
  }

  *keys(): IterableIterator<string> {
    for (const shard of this.shards) {
      if (shard !== undefined) yield* shard.keys();
    }
  }

  *values(): IterableIterator<V> {
    for (const shard of this.shards) {
      if (shard !== undefined) yield* shard.values();
    }
  }

  *entries(): IterableIterator<[string, V]> {
    for (const shard of this.shards) {
      if (shard !== undefined) yield* shard.entries();
    }
  }

  [Symbol.iterator](): IterableIterator<[string, V]> {
    return this.entries();
  }

  forEach(callbackfn: (value: V, key: string, map: ReadonlyMap<string, V>) => void, thisArg?: unknown): void {
    for (const [key, value] of this.entries()) callbackfn.call(thisArg, value, key, this);
  }

  /** This map with `key` bound to `value`, sharing every untouched shard. */
  with(key: string, value: V): ShardedMap<V> {
    const index = shardOf(key);
    const shard = this.shards[index];
    const replaced = shard?.has(key) === true;
    const nextShard = new Map(shard);
    nextShard.set(key, value);
    const nextShards = this.shards.slice();
    nextShards[index] = nextShard;
    return new ShardedMap<V>(nextShards, replaced ? this.entryCount : this.entryCount + 1);
  }

  /** This map without `key`, sharing every untouched shard. Returns this instance when the key is absent. */
  without(key: string): ShardedMap<V> {
    const index = shardOf(key);
    const shard = this.shards[index];
    if (shard?.has(key) !== true) return this;
    const nextShards = this.shards.slice();
    if (shard.size === 1) {
      nextShards[index] = undefined;
    } else {
      const nextShard = new Map(shard);
      nextShard.delete(key);
      nextShards[index] = nextShard;
    }
    return new ShardedMap<V>(nextShards, this.entryCount - 1);
  }
}

/**
 * The sharded view of `map`: the same instance when it is already sharded, a
 * distribution of its entries otherwise. A foreign map pays one whole-map pass,
 * and only once - every writer in this store publishes a ShardedMap.
 */
export function shardedMap<V>(map: ReadonlyMap<string, V>): ShardedMap<V> {
  return map instanceof ShardedMap ? map : ShardedMap.from(map);
}
