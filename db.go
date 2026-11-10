// Neon Postgres（Love 项目）连接与建表。只用 database/sql + lib/pq。
// 服务启动时自动建表（CREATE TABLE IF NOT EXISTS），
// 这样 Botkeep 部署后第一次启动就把 love 的表建好，不用再手动跑 SQL。
package main

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/lib/pq"
)

// loveSchema 与 love-schema.sql 保持一致（那份是版本记录，这里是启动时实际跑的）。
const loveSchema = `
CREATE TABLE IF NOT EXISTS love_users (
  id             SERIAL PRIMARY KEY,
  username       TEXT NOT NULL UNIQUE,
  display_name   TEXT NOT NULL DEFAULT '',
  password_hash  TEXT NOT NULL,
  is_superadmin BOOLEAN NOT NULL DEFAULT FALSE,
  display_effect TEXT NOT NULL DEFAULT 'polaroid'
    CHECK (display_effect IN ('polaroid', 'grid', 'carousel', 'masonry', 'coverflow', 'justified', 'stack', 'scatter', 'tree', 'ring')),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE love_users ADD COLUMN IF NOT EXISTS is_superadmin BOOLEAN NOT NULL DEFAULT FALSE;
-- 展示效果加了新值，旧表的 CHECK 约束要跟着换（约束名是建表时自动生成的）。
ALTER TABLE love_users DROP CONSTRAINT IF EXISTS love_users_display_effect_check;
ALTER TABLE love_users ADD CONSTRAINT love_users_display_effect_check
  CHECK (display_effect IN ('polaroid', 'grid', 'carousel', 'masonry', 'coverflow', 'justified', 'stack', 'scatter', 'tree', 'ring'));
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
`

var db *sql.DB

// initDB 连 Neon Postgres 并建表。没配 DATABASE_URL 时返回 nil，love 的账户功能停用。
func initDB() *sql.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Printf("love: 未配 DATABASE_URL，账户与上传功能停用")
		return nil
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("love: 打开数据库失败: %v", err)
		return nil
	}
	// Neon 免费档计算资源小，连接池收紧。
	conn.SetMaxOpenConns(5)
	conn.SetMaxIdleConns(2)
	if err := conn.Ping(); err != nil {
		log.Printf("love: 连接 Neon 失败: %v", err)
		return nil
	}
	if _, err := conn.Exec(loveSchema); err != nil {
		log.Printf("love: 建表失败: %v", err)
		return nil
	}
	log.Printf("love: 已连上 Neon Postgres，表就绪")
	return conn
}
