-- 为 app_settings 增加 description 列:让每行配置在库里自解释(不必回查代码就知道这个 key 是什么意思)。
-- 权威说明来自代码里的配置注册表(settings registry),写入时一并落库;本列是注册表说明的持久化快照。
ALTER TABLE app_settings
    ADD COLUMN description TEXT NOT NULL DEFAULT '';
