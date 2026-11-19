ALTER TABLE users ADD COLUMN login_username_key BLOB;
CREATE INDEX users_app_login_username_key ON users(login_username_key)
WHERE isNewsletterOnly=0 AND login_username_key IS NOT NULL;
