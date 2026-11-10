-- love 站：账户、会话、照片。Postgres 18，跑在 Neon Love 项目的 production 分支。
-- 用直连串跑（迁移不用 pooled）：psql "$DATABASE_URL_UNPOOLED" -f love-schema.sql
-- 密码只存 PBKDF2-HMAC-SHA256 哈希（盐+迭代次数+哈希），绝不存明文。

CREATE TABLE IF NOT EXISTS love_users (
  id             SERIAL PRIMARY KEY,
  username       TEXT NOT NULL UNIQUE,
  display_name   TEXT NOT NULL DEFAULT '',
  password_hash  TEXT NOT NULL,
  is_superadmin BOOLEAN NOT NULL DEFAULT FALSE,
  display_effect TEXT NOT NULL DEFAULT 'polaroid'
    CHECK (display_effect IN ('polaroid', 'grid', 'carousel', 'masonry')),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 已建好的表补一列（幂等，表不存在时建表已含该列）。
ALTER TABLE love_users ADD COLUMN IF NOT EXISTS is_superadmin BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS love_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    INT NOT NULL REFERENCES love_users(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS love_sessions_user_idx ON love_sessions(user_id);

CREATE TABLE IF NOT EXISTS love_photos (
  id           SERIAL PRIMARY KEY,
  user_id      INT NOT NULL REFERENCES love_users(id) ON DELETE CASCADE,
  object_key   TEXT NOT NULL,
  filename     TEXT NOT NULL DEFAULT '',
  caption      TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  size_bytes   BIGINT NOT NULL DEFAULT 0,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS love_photos_user_idx ON love_photos(user_id, created_at DESC);
