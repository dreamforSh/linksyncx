/**
 * Runs `mapper` over `items` with at most `limit` calls in flight and resolves with the
 * results in input order. A rejection fails the whole run (like Promise.all); callers that
 * need per-item outcomes should settle inside the mapper.
 */
export async function mapWithConcurrency<T, R>(
  items: readonly T[],
  limit: number,
  mapper: (item: T, index: number) => Promise<R>
): Promise<R[]> {
  const results = new Array<R>(items.length)
  const size = Math.max(1, Math.floor(Number.isFinite(limit) ? limit : 1))
  let next = 0

  const worker = async () => {
    while (next < items.length) {
      const index = next++
      results[index] = await mapper(items[index], index)
    }
  }

  await Promise.all(Array.from({ length: Math.min(size, items.length) }, () => worker()))
  return results
}
