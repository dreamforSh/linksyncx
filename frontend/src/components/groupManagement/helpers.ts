export interface NamedMember {
  user_id: number
  username?: string
  email?: string
}

export function memberDisplayName(member: NamedMember): string {
  return member.username || member.email || `#${member.user_id}`
}

// 组管理员看不到用户 ID，只有超管的界面展示 #ID
export function memberSubtitle(member: NamedMember, showId = true): string {
  return [member.email, showId ? `#${member.user_id}` : ''].filter(Boolean).join(' · ')
}

// Array.from keeps surrogate pairs and CJK characters intact.
export function initialOf(name: string): string {
  const first = Array.from(name.trim())[0]
  return first ? first.toUpperCase() : '?'
}

export function isValidLimit(value: unknown, min: number): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= min
}

// 组内美元上限：非负、有限，0 表示不限；与后端上界一致
export const MAX_GROUP_USD_LIMIT = 1_000_000_000

export function isValidUSDLimit(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= MAX_GROUP_USD_LIMIT
}

// 进度条里的美元金额：去掉多余的 0，避免窄列换行（$5、$3.37、$0.000123）
export function formatUsdCompact(value: number): string {
  if (!Number.isFinite(value)) return '$0'
  const digits = value !== 0 && Math.abs(value) < 0.01 ? 6 : 2
  return `$${value.toLocaleString('en-US', { maximumFractionDigits: digits })}`
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
