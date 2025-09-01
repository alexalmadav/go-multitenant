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
- **`database/schema_test.go`** - Tests for schema management operations
- **`database/migration_manager_test.go`** - Tests for migration management

### 2. Integration Tests

- **`integration_test.go`** - End-to-end tests requiring a PostgreSQL database

## Running Tests

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
# Set database URL (optional - defaults shown)
export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"

# Run all tests including integration tests
go test ./...

# Run only integration tests
go test . -run TestIntegration
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

- **Schema Naming**: Tests schema name generation
- **Schema Operations**: Tests create, drop, exists operations (mocked)
- **Search Path**: Tests PostgreSQL search path setting (mocked)
- **Schema Listing**: Tests tenant schema enumeration (mocked)

### 7. Migration Manager (`database/migration_manager_test.go`)

- **File Operations**: Tests loading migrations from filesystem
- **Migration Application**: Tests applying migrations (mocked for DB operations)
- **Rollback Operations**: Tests migration rollback (mocked)
- **File Listing**: Tests discovering migration files
- **Migration Tracking**: Tests applied migration tracking (mocked)

### 8. Integration Tests (`integration_test.go`)

- **Full Lifecycle**: Tests complete tenant lifecycle with real database
- **Schema Isolation**: Tests that tenant data is properly isolated
- **Concurrent Operations**: Tests thread safety with multiple tenants
- **Resolver Integration**: Tests tenant resolution with real data
- **Master Tables**: Tests automatic creation of management tables

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
docker run --name test-postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=test_multitenant -p 5432:5432 -d postgres:13

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
