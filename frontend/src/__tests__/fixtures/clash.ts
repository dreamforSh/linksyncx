import type { ClashPoolSettings } from '@/types'

export function makeClashPoolSettings(overrides: Partial<ClashPoolSettings> = {}): ClashPoolSettings {
  return {
    max_accounts_per_exit: 1,
    allow_unprobed_exit_binding: false,
    automatic_probes_enabled: true,
    health_test_url: 'https://example.com/health',
    health_timeout_ms: 5000,
    bound_check_interval_seconds: 60,
    unbound_check_interval_seconds: 600,
    failure_threshold: 3,
    recovery_threshold: 2,
    exit_probe_interval_minutes: 60,
    exit_probe_per_minute: 10,
    platform_checks_enabled: true,
    exit_change_policy: 'pause',
    pause_ttl_minutes: 30,
    missing_retention_days: 7,
    drop_protection_percent: 50,
    default_user_agent: 'clash.meta',
    ...overrides
  }
}
