-- Clash 订阅支持直接粘贴节点分享链接（ss:// vmess:// trojan:// 等，可以只有一个节点）：
-- 与本地文件一样把内容加密存进 content_encrypted、按需重新解析，只需放宽来源类型约束。

ALTER TABLE clash_profiles DROP CONSTRAINT IF EXISTS clash_profiles_source_type_check;
ALTER TABLE clash_profiles
    ADD CONSTRAINT clash_profiles_source_type_check CHECK (source_type IN ('url', 'file', 'links'));
