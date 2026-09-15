-- Migration: 240_restore_kiro_platform_constraints
--
-- 兜底：确保 user_platform_quotas.platform 与 composite_model_routes.target_platform
-- 的 CHECK 约束里含 fork 独有的 kiro 平台。
--
-- 背景（v0.3.4 线上事故，与 v0.3.1 的 237 事故同型）：
-- 上游 238_opencode_go_platform.sql 用无幂等守卫的裸 DROP+ADD 重建这两个约束，
-- 白名单取自上游平台列表，丢掉了 234_kiro_platform.sql 加入、237 修复时保留的
-- kiro。已有 platform='kiro' 行的部署在 ADD CONSTRAINT 校验时失败 → 启动
-- crash-loop；238 已修复为含 kiro，能救这类部署（失败的迁移在事务里回滚、未记账，
-- 重启会用修复后的内容重跑）。
--
-- 但另一类部署救不到：表里当时没有 kiro 行（例如全新部署）的库，238 的
-- kiro-less 版本会**成功**应用并记账，checksum 兼容规则随后让它一路跳过 ——
-- 约束里将永远没有 kiro，直到有人配置 kiro 平台限额才炸。本迁移就是补这一类。
--
-- 幂等守卫：只在约束定义里查不到 kiro 时才重建，重复执行无副作用。
-- 排在 238_purge_unlimited_user_platform_quotas.sql 之后，该迁移已清掉无限额
-- 占位行，故此处 ADD CONSTRAINT 不会因存量行违约。
DO $$
DECLARE
    quota_constraint_def TEXT;
    route_constraint_def TEXT;
BEGIN
    SELECT pg_get_constraintdef(c.oid)
      INTO quota_constraint_def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
     WHERE t.relname = 'user_platform_quotas'
       AND c.conname = 'user_platform_quotas_platform_check';

    IF quota_constraint_def IS NULL OR position('kiro' IN quota_constraint_def) = 0 THEN
        ALTER TABLE user_platform_quotas
            DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;
        ALTER TABLE user_platform_quotas
            ADD CONSTRAINT user_platform_quotas_platform_check
            CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                                'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'kiro'));
    END IF;

    SELECT pg_get_constraintdef(c.oid)
      INTO route_constraint_def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
     WHERE t.relname = 'composite_model_routes'
       AND c.conname = 'composite_model_routes_target_platform_check';

    IF route_constraint_def IS NULL OR position('kiro' IN route_constraint_def) = 0 THEN
        ALTER TABLE composite_model_routes
            DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;
        ALTER TABLE composite_model_routes
            ADD CONSTRAINT composite_model_routes_target_platform_check
            CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                                       'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'kiro'));
    END IF;
END $$;
