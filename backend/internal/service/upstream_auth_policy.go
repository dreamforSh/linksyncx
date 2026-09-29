package service

import "net/http"

func isTerminalClaudeAuthorizationError(account *Account, status int) bool {
	return account != nil && account.Platform == PlatformAnthropic && (status == http.StatusUnauthorized || status == http.StatusForbidden)
}
