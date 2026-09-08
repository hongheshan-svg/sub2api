-- 把 MiniMax 加入国产供应商平台白名单：
--   1. user_platform_quotas.platform CHECK
--   2. composite_model_routes.target_platform CHECK
--   3. channel_monitors / channel_monitor_request_templates.provider CHECK
--
-- 与 224/226/227 同型：DROP IF EXISTS 后重建超集约束，存量行瞬时校验通过。
--
-- fork 补丁：本迁移是上游原文，原文的两个 CHECK 重建时漏带了 fork 专属的
-- kiro 平台（234_kiro_platform.sql 加入的），导致任何已有 platform='kiro'
-- 行的部署在 ADD CONSTRAINT 时立刻违反新约束而迁移失败、进程重启循环
-- （v0.3.1 线上事故）。这里把 kiro 与 minimax 一起放进超集，保持两个 fork
-- 补丁互不覆盖。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'minimax', 'kiro'));

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;

ALTER TABLE composite_model_routes
    ADD CONSTRAINT composite_model_routes_target_platform_check
    CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                               'kimi', 'zhipu', 'deepseek', 'minimax', 'kiro'));

DO $$
DECLARE
    monitor_constraint_def TEXT;
    template_constraint_def TEXT;
BEGIN
    SELECT pg_get_constraintdef(c.oid)
      INTO monitor_constraint_def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
     WHERE t.relname = 'channel_monitors'
       AND c.conname = 'channel_monitors_provider_check';

    IF monitor_constraint_def IS NULL OR position('minimax' IN monitor_constraint_def) = 0 THEN
        ALTER TABLE channel_monitors
            DROP CONSTRAINT IF EXISTS channel_monitors_provider_check;
        ALTER TABLE channel_monitors
            ADD CONSTRAINT channel_monitors_provider_check
            CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok',
                                'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax'));
    END IF;

    SELECT pg_get_constraintdef(c.oid)
      INTO template_constraint_def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
     WHERE t.relname = 'channel_monitor_request_templates'
       AND c.conname = 'channel_monitor_request_templates_provider_check';

    IF template_constraint_def IS NULL OR position('minimax' IN template_constraint_def) = 0 THEN
        ALTER TABLE channel_monitor_request_templates
            DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check;
        ALTER TABLE channel_monitor_request_templates
            ADD CONSTRAINT channel_monitor_request_templates_provider_check
            CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok',
                                'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax'));
    END IF;
END $$;
