-- 002_seed_superadmin.sql — placeholder, API will ensure superadmin exists
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM users WHERE username='superadmin') THEN
    INSERT INTO users(username, password_hash, must_change_password, role_id, tenant_id)
    SELECT 'superadmin', '$2a$10$placeholderhashplaceholderhash1234567890123456789012', true, r.id, t.id
    FROM tenants t, roles r WHERE t.name='default' AND r.name='SuperAdmin' LIMIT 1;
  END IF;
END$$;
