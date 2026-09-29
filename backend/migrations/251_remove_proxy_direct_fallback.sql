-- 移除代理过期回退策略 "direct"（过期后把账号改投直连）。
-- 分配了代理 / Clash 出口的账号任何情况下都不得静默转为直连，否则会泄漏网关真实出口 IP：
-- 代理过期后账号要么保持绑定原代理（none，请求 fail-closed），要么改投到指定的备用代理（proxy）。

-- 1) 存量 direct 配置（以及旧导入写入的其它非法取值）一律改为 none。
UPDATE proxies SET fallback_mode = 'none', updated_at = NOW()
WHERE fallback_mode NOT IN ('none', 'proxy');

-- 2) 已被过期扫描改投成直连的账号切回原代理（原代理仍存在时），语义与后台「切回原代理」一致：
--    API key 账号清掉按网络身份缓存的上游计费探测快照，并逐个通知调度缓存刷新，切回立即生效。
WITH restored AS (
    UPDATE accounts AS a SET
        extra = CASE WHEN a.type = 'apikey' THEN a.extra - 'upstream_billing_probe' ELSE a.extra END,
        proxy_id = a.proxy_fallback_origin_id,
        proxy_fallback_origin_id = NULL,
        updated_at = NOW()
    WHERE a.proxy_id IS NULL
      AND a.proxy_fallback_origin_id IS NOT NULL
      AND a.deleted_at IS NULL
      AND EXISTS (
          SELECT 1 FROM proxies AS p
          WHERE p.id = a.proxy_fallback_origin_id AND p.deleted_at IS NULL
      )
    RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id FROM restored;

-- 3) 数据库层面禁止再写入 none / proxy 以外的取值。
ALTER TABLE proxies DROP CONSTRAINT IF EXISTS proxies_fallback_mode_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_fallback_mode_check CHECK (fallback_mode IN ('none', 'proxy'));
