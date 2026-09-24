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
