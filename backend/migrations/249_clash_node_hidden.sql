-- 管理员可以隐藏 Clash 节点：隐藏的节点不出现在默认节点列表和账号的代理选择器里，
-- 并且始终处于禁用状态（订阅刷新不会让它重新上线），取消隐藏后才恢复。
-- 独立一列而不是新的节点状态，这样刷新、失效、回收等现有状态流转都不受影响。

ALTER TABLE clash_nodes ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT FALSE;
