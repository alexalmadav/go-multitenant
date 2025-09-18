-- Remove indexes
DROP INDEX IF EXISTS idx_tenants_metadata_custom_domain;
DROP INDEX IF EXISTS idx_tenants_metadata_stripe_customer;
DROP INDEX IF EXISTS idx_tenants_metadata_gin;

-- Remove metadata column
ALTER TABLE tenants DROP COLUMN IF EXISTS metadata;
