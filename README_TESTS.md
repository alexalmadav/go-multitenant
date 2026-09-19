# Go MultiTenant - Test Suite

This document describes the comprehensive test suite for the go-multitenant package.

## Test Structure

The test suite is organized into several categories:

### 1. Unit Tests

#### Core Package Tests
- **`multitenant_test.go`** - Tests for the main MultiTenant struct and factory functions
- **`test_helpers.go`** - Mock implementations and test utilities

#### Tenant Package Tests  
- **`tenant/models_test.go`** - Tests for data models, validation functions, and constants
- **`tenant/flexible_limits_test.go`** - Tests for the flexible limits system
- **`tenant/resolver_test.go`** - Tests for tenant resolution from HTTP requests
- **`tenant/manager_test.go`** - Tests for tenant management operations
- **`tenant/limit_checker_test.go`** - Tests for limit checking and enforcement

#### Database Package Tests
- **`database/schema_test.go`** - Schema naming and quoting (pure functions)
- **`database/migration_manager_test.go`** - Migration file discovery and loading

#### Middleware Tests
- **`middleware/gin/middleware_test.go`** - Gin middleware behaviour with stubbed Manager/Resolver (httptest)

### 2. Integration Tests

Both files live in the root package and share one database discovery helper
(`newTestDB`): `TEST_DATABASE_URL` if set, otherwise a local PostgreSQL on
`localhost:5432`, otherwise a `postgres:16-alpine` container started with
testcontainers (Docker required). They skip only under `-short` or when no
database can be found.

Tenants are provisioned from `testdata/migrations` (projects, tenant_users)
via `testConfig`.

- **`database_integration_test.go`** - Schema isolation, search_path safety, pooler-safe tenant connections, migrations, limit enforcement, schema listing

CI runs the root integration suite twice: once against PostgreSQL directly
and once through PgBouncer in transaction mode (`TEST_DATABASE_URL` pointed at
the pooler). Locally you can do the same with a PgBouncer container:

```bash
docker run -d --name pgbouncer --add-host=host.docker.internal:host-gateway -p 6432:5432 \
  -e DB_HOST=host.docker.internal -e DB_USER=postgres -e DB_PASSWORD=postgres \
  -e DB_NAME=test_multitenant -e POOL_MODE=transaction -e AUTH_TYPE=scram-sha-256 \
  edoburu/pgbouncer
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:6432/test_multitenant?sslmode=disable' go test -count=1 .
```
- **`integration_test.go`** - Tenant lifecycle, resolver with real data, concurrent creation

## Running Tests

### Modules

The repository is a Go workspace (`go.work`) of three modules. Run tests
module by module:

- Root (`go test ./...`): core, `database`, `middleware/httpmw` and the
  integration suites.
- `cd middleware/gin && go test ./...`: the Gin adapter and its one
  integration test.
- `cd examples && go test ./...`: the Stripe example.

When modules share a single database, as the root and `middleware/gin`
integration tests do here, run them one at a time rather than in parallel.

### Unit Tests Only

```bash
# Run all unit tests (fast, no database required)
go test ./... -short

# Run tests for specific package
go test ./tenant -v

# Run specific test
go test ./tenant -run TestValidateStatus
```

### Integration Tests

Integration tests require a PostgreSQL database. Set up the database and environment:

```bash
# Optional: point at an existing database. Without it, a local PostgreSQL on
# localhost:5432 is used if present, otherwise a container is started via Docker.
export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"

# Run all tests including integration tests
go test ./...

# Run only integration tests
go test . -run 'TestIntegration|TestDatabase'
```

### Test Coverage

```bash
# Generate coverage report
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

## Test Categories

### 1. Models and Validation (`tenant/models_test.go`)

- **Constants Testing**: Validates status and plan type constants
- **Validation Functions**: Tests `ValidateStatus()` and `ValidatePlanType()`
- **Struct Validation**: Tests tenant data validation
- **Error Types**: Tests `ValidationError` and `TenantError`
- **Default Configuration**: Tests `DefaultConfig()` function

### 2. Flexible Limits (`tenant/flexible_limits_test.go`)

- **LimitValue Types**: Tests all limit value types (int, float, string, bool, duration)
- **Type Conversions**: Tests conversion methods and error handling
- **Unlimited Values**: Tests unlimited value detection
- **FlexibleLimits Map**: Tests CRUD operations on limits
- **Type-Safe Getters**: Tests strongly-typed getter methods

### 3. Tenant Resolver (`tenant/resolver_test.go`)

- **Subdomain Resolution**: Tests extracting tenant from subdomains
- **Path Resolution**: Tests extracting tenant from URL paths
- **Header Resolution**: Tests extracting tenant from HTTP headers
- **Validation**: Tests subdomain format validation
- **Error Handling**: Tests various error conditions

### 4. Tenant Manager (`tenant/manager_test.go`)

- **CRUD Operations**: Tests create, read, update, delete operations
- **Tenant Lifecycle**: Tests provisioning, suspension, activation
- **Access Validation**: Tests user access validation
- **Limit Checking**: Tests limit enforcement
- **Context Management**: Tests tenant context creation

### 5. Limit Checker (`tenant/limit_checker_test.go`)

- **Limit Enforcement**: Tests various limit types and enforcement
- **Plan Management**: Tests plan limit configuration
- **Schema Management**: Tests limit schema operations
- **Usage Tracking**: Tests usage tracker integration
- **Validation Logic**: Tests type-specific validation methods

### 6. Schema Manager (`database/schema_test.go`)

- **Schema Naming**: Tests schema name generation and identifier quoting
- Create/drop/exists/list are covered against a real database in `database_integration_test.go`

### 7. Migration Manager (`database/migration_manager_test.go`)

- **File Operations**: Tests loading migrations from filesystem
- **File Listing**: Tests discovering migration files
- Apply, rollback, idempotency and bulk apply are covered against a real database in `database_integration_test.go`

### 8. Middleware (`middleware/gin/middleware_test.go`)

- **ResolveTenant**: Skip paths, unresolvable tenant, context population
- **ValidateTenant**: Status checks and authentication requirement
- **EnforceLimits**: Limit violations map to 402 `PLAN_LIMIT_EXCEEDED`; other failures to 500

### 9. Integration Tests (`integration_test.go`, `database_integration_test.go`)

- **Full Lifecycle**: Complete tenant lifecycle with real database
- **Schema Isolation**: Tenant tables, indexes, functions and triggers live only in the tenant schema
- **Connection Safety**: `GetTenantConn` resets search_path on close; `WithTenantTx` isolation under concurrency
- **Migrations**: Apply, idempotent re-apply, failure not recorded, rollback, bulk apply with partial failure
- **Provisioning**: `ProvisionTenant` applies fixture migrations in order and records them; re-provisioning after a failing migration resumes and completes; an empty `MigrationsDir` yields an empty schema; `ApplyPendingToAllTenants` brings an older, already-active tenant up to date
- **Metadata**: Round-trips through Create/Get/Update/List and `FindByMetadata`; the `metadata` column is added correctly to a pre-existing `tenants` table
- **Limits**: Default usage tracker rejects usage above plan limits; counts a configured table and skips an unconfigured limit
- **Hooks**: `TestDatabase_Hooks_ProvisionedHookCanPersistMetadata` proves a hook can persist metadata via `UpdateTenant` from inside `OnTenantProvisioned`, against a real database, through `CreateTenant` and `ProvisionTenant`. The full create/provision/update/status-change/delete event matrix is covered by unit tests in `tenant/hooks_test.go` against a mock repository
- **Resolver Integration**: Tenant resolution with real data

## Mock Objects

The test suite includes comprehensive mock implementations:

- **MockRepository**: In-memory tenant data storage
- **MockSchemaManager**: Mock schema operations
- **MockMigrationManager**: Mock migration tracking
- **MockLimitChecker**: Mock limit enforcement
- **MockUsageTracker**: Mock usage tracking

## Test Database Setup

For integration tests, you need a PostgreSQL database:

### Docker Setup

```bash
# Start PostgreSQL in Docker
docker run --name test-postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=test_multitenant -p 5432:5432 -d postgres:16

# Set environment variable
export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"
```

### Local Setup

```sql
-- Create test database
CREATE DATABASE test_multitenant;
CREATE USER test_user WITH PASSWORD 'test_password';
GRANT ALL PRIVILEGES ON DATABASE test_multitenant TO test_user;
```

## Best Practices

### 1. Test Isolation

- Each test cleans up after itself
- Integration tests use unique tenant IDs
- Database state is reset between tests

### 2. Error Testing

- Tests cover both success and failure cases
- Error messages are validated
- Edge cases are explicitly tested

### 3. Mock Usage

- Unit tests use mocks for external dependencies
- Integration tests use real database connections
- Mocks are kept simple and focused

### 4. Test Data

- Uses realistic test data
- Tests edge cases (empty strings, nil values, etc.)
- Uses generated UUIDs for uniqueness

## Running Specific Test Suites

```bash
# Core functionality only
go test . -short

# Tenant management
go test ./tenant

# Database operations (unit tests only)
go test ./database -short

# All unit tests
go test ./... -short

# All tests including integration
go test ./...

# Verbose output
go test ./... -v

# With race detection
go test ./... -race

# Benchmark tests
go test ./... -bench=.
```

## Test Maintenance

- Add tests for new features
- Update mocks when interfaces change
- Keep integration tests minimal but comprehensive
- Document test database requirements
- Update this README when adding new test categories
