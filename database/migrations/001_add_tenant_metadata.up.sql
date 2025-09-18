-- Add metadata column to tenants table
ALTER TABLE tenants 
ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}';

-- Create GIN index for efficient JSONB queries
CREATE INDEX IF NOT EXISTS idx_tenants_metadata_gin 
ON tenants USING GIN (metadata);

-- Create specific indexes for common metadata queries
CREATE INDEX IF NOT EXISTS idx_tenants_metadata_stripe_customer 
ON tenants USING BTREE ((metadata->>'stripe_customer_id')) 
WHERE metadata ? 'stripe_customer_id';

CREATE INDEX IF NOT EXISTS idx_tenants_metadata_custom_domain 
ON tenants USING BTREE ((metadata->>'custom_domain')) 
WHERE metadata ? 'custom_domain';

-- Add a comment explaining the metadata column
COMMENT ON COLUMN tenants.metadata IS 'JSONB field for extensible tenant metadata including integrations like Stripe, custom domains, etc.';
