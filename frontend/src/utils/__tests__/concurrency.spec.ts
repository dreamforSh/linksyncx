import { describe, expect, it } from 'vitest'
import { mapWithConcurrency } from '../concurrency'

const tick = () => new Promise((resolve) => setTimeout(resolve, 0))

describe('mapWithConcurrency', () => {
  it('never runs more than the limit at once and keeps the input order', async () => {
    let active = 0
    let peak = 0
    const results = await mapWithConcurrency([5, 1, 4, 2, 3, 0], 2, async (value, index) => {
      active++
      peak = Math.max(peak, active)
      for (let i = 0; i < value; i++) await tick()
      active--
      return `${index}:${value}`
    })
    expect(peak).toBe(2)
    expect(results).toEqual(['0:5', '1:1', '2:4', '3:2', '4:3', '5:0'])
  })

  it('handles empty input and clamps invalid limits to one worker', async () => {
    await expect(mapWithConcurrency([], 4, async () => 1)).resolves.toEqual([])

    let active = 0
    let peak = 0
    await mapWithConcurrency([1, 2, 3], 0, async () => {
      active++
      peak = Math.max(peak, active)
      await tick()
      active--
    })
    expect(peak).toBe(1)
  })

  it('rejects when a mapper rejects', async () => {
    await expect(
      mapWithConcurrency([1, 2], 4, async (value) => {
        if (value === 2) throw new Error('boom')
        return value
      })
    ).rejects.toThrow('boom')
  })
})
