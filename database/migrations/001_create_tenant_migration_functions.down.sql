-- Drop tenant migration functions in reverse order

DROP FUNCTION IF EXISTS validate_tenant_schema(UUID);
DROP FUNCTION IF EXISTS get_tenant_applied_migrations(UUID);
DROP FUNCTION IF EXISTS is_tenant_migration_applied(UUID, VARCHAR(50));
DROP FUNCTION IF EXISTS get_tenant_schema_name(UUID);
DROP FUNCTION IF EXISTS apply_migration_to_all_tenants(VARCHAR(50), VARCHAR(255), TEXT, TEXT);
DROP FUNCTION IF EXISTS rollback_tenant_migration(UUID, VARCHAR(50));
DROP FUNCTION IF EXISTS apply_tenant_migration(UUID, VARCHAR(50), VARCHAR(255), TEXT, TEXT);
