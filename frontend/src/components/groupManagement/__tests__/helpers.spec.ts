import { describe, expect, it } from 'vitest'

import { exceedsBalance, generatePassword, isValidEmail, isValidPassword } from '../helpers'

describe('group management helpers', () => {
  it('generates readable random passwords that pass validation', () => {
    const first = generatePassword()
    const second = generatePassword()
    expect(first).toHaveLength(14)
    expect(first).not.toBe(second)
    expect(first).toMatch(/^[A-HJ-NP-Za-km-z2-9]+$/)
    expect(isValidPassword(first)).toBe(true)
  })

  it('validates passwords like the backend (6 chars, at most 72 bytes)', () => {
    expect(isValidPassword('12345')).toBe(false)
    expect(isValidPassword('123456')).toBe(true)
    expect(isValidPassword('a'.repeat(72))).toBe(true)
    expect(isValidPassword('a'.repeat(73))).toBe(false)
    // 多字节字符按字节计：25 个汉字 = 75 字节
    expect(isValidPassword('密'.repeat(25))).toBe(false)
  })

  it('validates email shape', () => {
    expect(isValidEmail(' kim@example.org ')).toBe(true)
    expect(isValidEmail('kim@example')).toBe(false)
    expect(isValidEmail('kim example.org')).toBe(false)
  })

  it('compares balances with a small tolerance', () => {
    expect(exceedsBalance(10, 10)).toBe(false)
    expect(exceedsBalance(0.1 + 0.2, 0.3)).toBe(false)
    expect(exceedsBalance(10.01, 10)).toBe(true)
  })
})
