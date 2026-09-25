export interface NamedMember {
  user_id: number
  username?: string
  email?: string
}

export function memberDisplayName(member: NamedMember): string {
  return member.username || member.email || `#${member.user_id}`
}

export function memberSubtitle(member: NamedMember): string {
  return [member.email, `#${member.user_id}`].filter(Boolean).join(' · ')
}

// Array.from keeps surrogate pairs and CJK characters intact.
export function initialOf(name: string): string {
  const first = Array.from(name.trim())[0]
  return first ? first.toUpperCase() : '?'
}

export function isValidLimit(value: unknown, min: number): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= min
}

export type QuotaTone = 'normal' | 'warning' | 'danger'

export function quotaPercent(used: number, limit: number): number {
  if (limit <= 0) return 0
  return Math.min(100, Math.round((used / limit) * 100))
}

export function quotaTone(used: number, limit: number): QuotaTone {
  if (limit <= 0) return 'normal'
  const ratio = used / limit
  if (ratio >= 0.9) return 'danger'
  if (ratio >= 0.7) return 'warning'
  return 'normal'
}

// 去掉易混淆字符（0/O、1/l/I），方便线下转交
const PASSWORD_ALPHABET = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789'

export function generatePassword(length = 14): string {
  const values = new Uint32Array(length)
  crypto.getRandomValues(values)
  return Array.from(values, value => PASSWORD_ALPHABET[value % PASSWORD_ALPHABET.length]).join('')
}

// 与后端一致：至少 6 个字符，bcrypt 最多接受 72 字节
export function isValidPassword(password: string): boolean {
  return password.length >= 6 && new TextEncoder().encode(password).length <= 72
}

export function isValidEmail(email: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())
}

// 余额以 8 位小数存储，比较时留出浮点误差
export function exceedsBalance(amount: number, available: number): boolean {
  return amount - available > 1e-9
}
