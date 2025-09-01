-- Migration functions for multi-tenant database management
-- Copied and adapted from the constructor-mx backend

-- Function to apply tenant migration (FIXED VERSION)
CREATE OR REPLACE FUNCTION apply_tenant_migration(
    tenant_uuid UUID,
    migration_version VARCHAR(50),
    migration_name VARCHAR(255),
    migration_sql TEXT,
    rollback_sql TEXT DEFAULT NULL
) RETURNS VOID AS $$
DECLARE
    schema_name TEXT;
    migration_checksum VARCHAR(64);
BEGIN
    schema_name := 'tenant_' || tenant_uuid::TEXT;
    migration_checksum := encode(digest(migration_sql, 'sha256'), 'hex');
    
    -- Check if migration already applied
    IF EXISTS (
        SELECT 1 FROM public.tenant_migrations 
        WHERE tenant_id = tenant_uuid AND public.tenant_migrations.migration_version = apply_tenant_migration.migration_version
    ) THEN
        RAISE NOTICE 'Migration % already applied for tenant %', migration_version, tenant_uuid;
        RETURN;
    END IF;
    
    -- Check if schema exists
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.schemata 
        WHERE schema_name = schema_name
    ) THEN
        RAISE EXCEPTION 'Tenant schema % does not exist', schema_name;
    END IF;
    
    -- Apply migration
    EXECUTE 'SET search_path TO ' || quote_ident(schema_name) || ', public';
    EXECUTE migration_sql;
    
    -- Record migration (explicitly reference public schema)
    INSERT INTO public.tenant_migrations (
        id, tenant_id, migration_version, migration_name, 
        rollback_sql, checksum, applied_at
    ) VALUES (
        gen_random_uuid(), tenant_uuid, migration_version, migration_name, 
        rollback_sql, migration_checksum, CURRENT_TIMESTAMP
    );
    
    SET search_path TO public;
    
    RAISE NOTICE 'Migration % applied successfully for tenant %', migration_version, tenant_uuid;
END;
$$ LANGUAGE plpgsql;

-- Function to rollback tenant migration (FIXED VERSION)
CREATE OR REPLACE FUNCTION rollback_tenant_migration(
    tenant_uuid UUID,
    migration_version VARCHAR(50)
) RETURNS VOID AS $$
DECLARE
    schema_name TEXT;
    rollback_sql_text TEXT;
BEGIN
    schema_name := 'tenant_' || tenant_uuid::TEXT;
    
    -- Get rollback SQL (explicitly reference public schema)
    SELECT rollback_sql INTO rollback_sql_text
    FROM public.tenant_migrations
    WHERE tenant_id = tenant_uuid AND public.tenant_migrations.migration_version = rollback_tenant_migration.migration_version;
    
    IF rollback_sql_text IS NULL THEN
        RAISE EXCEPTION 'No rollback SQL found for migration % and tenant %', migration_version, tenant_uuid;
    END IF;
    
    -- Execute rollback
    EXECUTE 'SET search_path TO ' || quote_ident(schema_name) || ', public';
    EXECUTE rollback_sql_text;
    
    -- Remove migration record (explicitly reference public schema)
    DELETE FROM public.tenant_migrations 
    WHERE tenant_id = tenant_uuid AND public.tenant_migrations.migration_version = rollback_tenant_migration.migration_version;
    
    SET search_path TO public;
    
    RAISE NOTICE 'Migration % rolled back successfully for tenant %', migration_version, tenant_uuid;
END;
$$ LANGUAGE plpgsql;

-- Function to apply migration to all tenants
CREATE OR REPLACE FUNCTION apply_migration_to_all_tenants(
    migration_version VARCHAR(50),
    migration_name VARCHAR(255),
    migration_sql TEXT,
    rollback_sql TEXT DEFAULT NULL
) RETURNS VOID AS $$
DECLARE
    tenant_record RECORD;
    success_count INTEGER := 0;
    error_count INTEGER := 0;
    total_tenants INTEGER := 0;
BEGIN
    -- Count total active tenants
    SELECT COUNT(*) INTO total_tenants 
    FROM tenants 
    WHERE status = 'active';
    
    RAISE NOTICE 'Starting migration % for % active tenants', migration_version, total_tenants;
    
    FOR tenant_record IN SELECT id, name FROM tenants WHERE status = 'active' LOOP
        BEGIN
            PERFORM apply_tenant_migration(
                tenant_record.id,
                migration_version,
                migration_name,
                migration_sql,
                rollback_sql
            );
            success_count := success_count + 1;
            
        EXCEPTION WHEN OTHERS THEN
            error_count := error_count + 1;
            RAISE WARNING 'Migration % failed for tenant % (%): %', 
                migration_version, tenant_record.name, tenant_record.id, SQLERRM;
        END;
    END LOOP;
    
    IF error_count > 0 THEN
        RAISE EXCEPTION 'Migration % completed with errors: % succeeded, % failed out of % tenants', 
            migration_version, success_count, error_count, total_tenants;
    ELSE
        RAISE NOTICE 'Migration % applied successfully to all % tenants', 
            migration_version, success_count;
    END IF;
END;
$$ LANGUAGE plpgsql;

-- Function to get tenant schema name
CREATE OR REPLACE FUNCTION get_tenant_schema_name(tenant_uuid UUID)
RETURNS TEXT AS $$
BEGIN
    RETURN 'tenant_' || tenant_uuid::TEXT;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- Function to check if tenant migration is applied
CREATE OR REPLACE FUNCTION is_tenant_migration_applied(
    tenant_uuid UUID,
    migration_version VARCHAR(50)
) RETURNS BOOLEAN AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1 FROM public.tenant_migrations 
        WHERE tenant_id = tenant_uuid AND public.tenant_migrations.migration_version = migration_version
    );
END;
$$ LANGUAGE plpgsql;

-- Function to list applied migrations for a tenant
CREATE OR REPLACE FUNCTION get_tenant_applied_migrations(tenant_uuid UUID)
RETURNS TABLE(
    migration_id UUID,
    migration_version VARCHAR(50),
    migration_name VARCHAR(255),
    applied_at TIMESTAMP WITH TIME ZONE,
    checksum VARCHAR(64)
) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        tm.id,
        tm.migration_version,
        tm.migration_name,
        tm.applied_at,
        tm.checksum
    FROM public.tenant_migrations tm
    WHERE tm.tenant_id = tenant_uuid
    ORDER BY tm.applied_at;
END;
$$ LANGUAGE plpgsql;

-- Function to validate tenant schema exists
CREATE OR REPLACE FUNCTION validate_tenant_schema(tenant_uuid UUID)
RETURNS BOOLEAN AS $$
DECLARE
    schema_name TEXT;
BEGIN
    schema_name := get_tenant_schema_name(tenant_uuid);
    
    RETURN EXISTS (
        SELECT 1 FROM information_schema.schemata 
        WHERE schema_name = schema_name
    );
END;
$$ LANGUAGE plpgsql;
