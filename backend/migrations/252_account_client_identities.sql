-- 账号级客户端身份以数据库为真相源，Redis 指纹键（fingerprint:{id}）降为缓存。
-- 一行一个账号：device_id（Claude 为 metadata.user_id.device_id）与学习到的 UA / X-Stainless-* 头。
-- identity_epoch 只在管理员重置客户端身份、或账号换了主人（owner_ref 变化）时递增，参与上游会话映射。
-- owner_ref 是身份所属的上游账号主人（Claude 为 extra.account_uuid），空表示未知。
-- 存量账号的身份仍在 Redis 里，运行期首次读到时原样收编进本表，不在迁移里生成。

CREATE TABLE IF NOT EXISTS account_client_identities (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    platform VARCHAR(50) NOT NULL,
    identity_epoch BIGINT NOT NULL DEFAULT 0,
    device_id VARCHAR(128) NOT NULL,
    owner_ref VARCHAR(128) NOT NULL DEFAULT '',
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'account_client_identities_epoch_check') THEN
        ALTER TABLE account_client_identities
            ADD CONSTRAINT account_client_identities_epoch_check CHECK (identity_epoch >= 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'account_client_identities_device_check') THEN
        ALTER TABLE account_client_identities
            ADD CONSTRAINT account_client_identities_device_check CHECK (device_id <> '');
    END IF;
END $$;
