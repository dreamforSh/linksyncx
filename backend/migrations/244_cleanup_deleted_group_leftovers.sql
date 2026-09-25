-- 244: 清理已软删除分组遗留的权限与关联数据。
--
-- 分组删除是软删除（groups.deleted_at），各表外键上的 ON DELETE CASCADE / SET NULL 不会触发。
-- 删除流程此前没有清理组管理授权、成员、管控设置、渠道关联、用户专属倍率与兜底引用，
-- 这里一次性补清历史数据；之后由删除流程在同一事务内完成清理。迁移可重复执行。

-- 已删除管理分组创建的组用户，如果不再属于任何未删除的分组，按移除成员的规则停用账号。
-- 必须在删除成员行之前执行（依赖 group_members.owned）。
UPDATE users u
SET status = 'disabled', updated_at = NOW()
WHERE u.role = 'user'
  AND u.status = 'active'
  AND u.deleted_at IS NULL
  AND EXISTS (
      SELECT 1 FROM group_members gm JOIN groups g ON g.id = gm.group_id
      WHERE gm.user_id = u.id AND gm.owned AND g.deleted_at IS NOT NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM group_members gm JOIN groups g ON g.id = gm.group_id
      WHERE gm.user_id = u.id AND g.deleted_at IS NULL
  );

DELETE FROM group_managers x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

DELETE FROM group_members x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

DELETE FROM group_management_settings x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

DELETE FROM channel_groups x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

DELETE FROM user_group_rate_multipliers x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

DELETE FROM user_allowed_groups x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

DELETE FROM account_groups x USING groups g
WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL;

UPDATE groups
SET fallback_group_id = NULL, updated_at = NOW()
WHERE fallback_group_id IN (SELECT id FROM groups WHERE deleted_at IS NOT NULL);

UPDATE groups
SET fallback_group_id_on_invalid_request = NULL, updated_at = NOW()
WHERE fallback_group_id_on_invalid_request IN (SELECT id FROM groups WHERE deleted_at IS NOT NULL);
