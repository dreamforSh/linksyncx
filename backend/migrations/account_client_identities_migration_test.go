package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 252 号迁移建立账号客户端身份表：数据库是身份真相源，Redis 指纹键降为缓存。
// 存量身份在运行期收编，迁移本身不生成任何身份。真实 Postgres 语义另用 PGlite 验证过。
func TestAccountClientIdentitiesMigration(t *testing.T) {
	content, err := FS.ReadFile("252_account_client_identities.sql")
	require.NoError(t, err)

	var lines []string
	for _, line := range strings.Split(string(content), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			lines = append(lines, trimmed)
		}
	}
	sql := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS account_client_identities (")
	require.Contains(t, sql, "account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE")
	require.Contains(t, sql, "identity_epoch BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "device_id VARCHAR(128) NOT NULL")
	require.Contains(t, sql, "owner_ref VARCHAR(128) NOT NULL DEFAULT ''")
	require.Contains(t, sql, "headers JSONB NOT NULL DEFAULT '{}'::jsonb")
	require.Contains(t, sql, "CHECK (identity_epoch >= 0)")
	require.Contains(t, sql, "CHECK (device_id <> '')")
	require.NotContains(t, sql, "INSERT INTO account_client_identities", "existing identities are adopted at runtime, not generated here")
}
