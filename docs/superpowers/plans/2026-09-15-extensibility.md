# Extensibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tenants are provisioned from the application's migration files, carry a JSONB metadata map on the core `Tenant`, and emit lifecycle events to registered hooks; the unwired "extensible tenant" layer is removed.

**Architecture:** `ProvisionTenant` creates an empty schema and asks the migration manager to apply every pending `*.up.sql` from `MigrationsDir`. The base `Tenant`/`Repository` gain a `metadata` JSONB column. The `manager` keeps an ordered slice of `Hook`s and fires them after each committed write (and `ValidateMetadata` before). Usage tracking and `GetStats` stop assuming table names and read `LimitsConfig.UsageTables`.

**Tech Stack:** Go 1.24, pgx/v5 (stdlib, simple protocol), Gin, zap, testcontainers-go. Integration tests in the root package use `newTestDB(t)` from `database_integration_test.go`, which finds `TEST_DATABASE_URL`, then local Postgres on 5432, then a container.

**Spec:** `docs/superpowers/specs/2026-09-15-extensibility-design.md`

## Global Constraints

- Every task: `gofmt -l .` prints nothing, `go build ./... && go vet ./...` pass, `go test -short -race ./...` passes, and `go test -count=1 ./...` passes against a real Postgres before commit. Use `set -o pipefail` when piping `go test` into `grep`.
- Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Do not add a `go.mod` under `examples/`; all examples build under the root module.
- Never write a test that cannot fail; run each new test before implementing and confirm the failure message.
- Branch: `feat/extensibility` (already exists, contains the spec commit).
- Breaking changes are allowed (library is v0.6.0); they are listed in the spec.

---

## File map

| Path | Responsibility after this plan |
|---|---|
| `database/migration_manager.go` | Apply/rollback/list migrations; parse and sort migration files; `ApplyPending` |
| `database/schema.go` | Create/drop/list tenant schemas. No tables. |
| `database/postgres/repository.go` | CRUD on `public.tenants` including `metadata`; `FindByMetadata`; master tables |
| `database/postgres/usage_tracker.go` | Row counts for limits named in `UsageTables` |
| `tenant/interfaces.go` | `Manager`, `Repository`, `SchemaManager`, `MigrationManager` interfaces |
| `tenant/manager.go` | Lifecycle orchestration, hook firing, `GetStats` |
| `tenant/hooks.go` | `Hook`, `BaseHook`, `HookError` |
| `tenant/metadata.go` | `TenantMetadata` and the Stripe/Branding typed wrappers (moved from `extensible_models.go`) |
| `tenant/models.go` | `Tenant` (+`Metadata`), `Stats` (new shape), `LimitsConfig` (+`UsageTables`) |
| `multitenant.go` | Wiring, `MigrationsDir` validation, re-exports |
| `testdata/migrations/` | Fixture schema used by integration tests |
| `examples/stripe-integration/` | Runnable hook example with a unit test |
| Deleted | `tenant/extensible_interfaces.go`, `tenant/extensible_models.go`, `database/postgres/extensible_repository.go`, `examples/extensible-tenant/` |

---

### Task 1: Sorted migration file discovery and `ApplyPending`

**Files:**
- Modify: `database/migration_manager.go`
- Modify: `tenant/interfaces.go` (MigrationManager interface, lines ~81-88)
- Modify: `test_helpers.go` (`MockMigrationManager`), `tenant/manager_test.go` (`MockManagerMigrationManager`)
- Modify: `multitenant.go` (`New`: validate `MigrationsDir`)
- Test: `database/migration_manager_test.go`, `database_integration_test.go`, `multitenant_test.go`

**Interfaces:**
- Produces: `MigrationManager.ApplyPending(ctx, tenantID uuid.UUID) error`, `MigrationManager.ApplyPendingToAllTenants(ctx) error` on the `tenant.MigrationManager` interface; `ListMigrationFiles()` sorted.

- [ ] **Step 1: Write failing unit tests for sorting and parsing**

Append to `database/migration_manager_test.go`:

```go
func TestMigrationManager_ListMigrationFiles_SortedByFilename(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"010_late.up.sql", "002_second.up.sql", "001_first.up.sql", "001_first.down.sql"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("-- x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mgr := NewMigrationManager(nil, zaptest.NewLogger(t), dir, nil, nil).(*MigrationManager)

	got, err := mgr.ListMigrationFiles()
	if err != nil {
		t.Fatalf("ListMigrationFiles: %v", err)
	}
	want := []string{"001_first", "002_second", "010_late"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMigrationManager_ListMigrationFiles_RejectsBadNames(t *testing.T) {
	cases := map[string][]string{
		"no underscore":     {"001.up.sql"},
		"empty name":        {"001_.up.sql"},
		"orphan down file":  {"001_first.down.sql"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("-- x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			mgr := NewMigrationManager(nil, zaptest.NewLogger(t), dir, nil, nil).(*MigrationManager)
			if _, err := mgr.ListMigrationFiles(); err == nil {
				t.Errorf("expected error for %v", files)
			}
		})
	}
}
```

- [ ] **Step 2: Run them and confirm failure**

Run: `go test -count=1 -run 'TestMigrationManager_ListMigrationFiles_(SortedByFilename|RejectsBadNames)' ./database/`
Expected: `SortedByFilename` FAILS on ordering (`010_late` before `001_first` because `os.ReadDir` order is lexical on some filesystems but the test must still fail on at least one case; if it passes on your machine, `RejectsBadNames` will fail with "expected error"). At least one FAIL.

- [ ] **Step 3: Implement parsing, sorting and `ApplyPending`**

In `database/migration_manager.go`, add `"sort"` to imports. Replace `ListMigrationFiles` with:

```go
// migrationFile is one parsed <version>_<name>.up.sql entry.
type migrationFile struct {
	Version string
	Name    string
}

func (f migrationFile) base() string { return f.Version + "_" + f.Name }

// migrationFiles returns the migrations in migrationsDir sorted by filename.
// An empty migrationsDir yields no files and no error.
func (m *MigrationManager) migrationFiles() ([]migrationFile, error) {
	if m.migrationsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(m.migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var files []migrationFile
	ups := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".up.sql")
		version, name, ok := strings.Cut(base, "_")
		if !ok || version == "" || name == "" {
			return nil, fmt.Errorf("migration file %q must be named <version>_<name>.up.sql", e.Name())
		}
		files = append(files, migrationFile{Version: version, Name: name})
		ups[base] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		if base := strings.TrimSuffix(e.Name(), ".down.sql"); !ups[base] {
			return nil, fmt.Errorf("rollback file %q has no matching .up.sql", e.Name())
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].base() < files[j].base() })
	return files, nil
}

// ListMigrationFiles returns "<version>_<name>" for every migration file, sorted by filename.
func (m *MigrationManager) ListMigrationFiles() ([]string, error) {
	if m.migrationsDir == "" {
		return nil, fmt.Errorf("migrations directory not configured")
	}
	files, err := m.migrationFiles()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.base())
	}
	return out, nil
}

// ApplyPending applies every migration file not yet recorded for the tenant, in order.
func (m *MigrationManager) ApplyPending(ctx context.Context, tenantID uuid.UUID) error {
	files, err := m.migrationFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := m.ApplyMigrationFromFile(ctx, tenantID, f.Version, f.Name); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPendingToAllTenants runs ApplyPending for every active tenant and reports all failures.
func (m *MigrationManager) ApplyPendingToAllTenants(ctx context.Context) error {
	return m.forEachActiveTenant(ctx, "pending migrations", func(t *tenant.Tenant) error {
		return m.ApplyPending(ctx, t.ID)
	})
}

// forEachActiveTenant pages through active tenants, applies fn to each, and
// returns a joined error naming every tenant that failed.
func (m *MigrationManager) forEachActiveTenant(ctx context.Context, what string, fn func(*tenant.Tenant) error) error {
	var errs []error
	applied := 0
	const perPage = 100
	for page := 1; ; page++ {
		tenants, _, err := m.repository.List(ctx, page, perPage)
		if err != nil {
			return fmt.Errorf("failed to list tenants: %w", err)
		}
		for _, t := range tenants {
			if t.Status != tenant.StatusActive {
				continue
			}
			if err := fn(t); err != nil {
				errs = append(errs, err)
				continue
			}
			applied++
		}
		if len(tenants) < perPage {
			break
		}
	}
	if len(errs) > 0 {
		m.logger.Error("Bulk operation completed with errors",
			zap.String("operation", what), zap.Int("succeeded", applied), zap.Int("failed", len(errs)))
		return fmt.Errorf("%s failed for %d tenant(s): %w", what, len(errs), errors.Join(errs...))
	}
	m.logger.Info("Bulk operation applied to all active tenants", zap.String("operation", what), zap.Int("count", applied))
	return nil
}
```

Rewrite the existing `ApplyToAllTenants` body to reuse the helper:

```go
func (m *MigrationManager) ApplyToAllTenants(ctx context.Context, migration *tenant.Migration) error {
	m.logger.Info("Applying migration to all active tenants",
		zap.String("migration_version", migration.Version),
		zap.String("migration_name", migration.Name))
	return m.forEachActiveTenant(ctx, "migration "+migration.Version, func(t *tenant.Tenant) error {
		return m.ApplyMigration(ctx, t.ID, migration)
	})
}
```

In `tenant/interfaces.go`, extend the interface:

```go
type MigrationManager interface {
	ApplyMigration(ctx context.Context, tenantID uuid.UUID, migration *Migration) error
	RollbackMigration(ctx context.Context, tenantID uuid.UUID, version string) error
	ApplyToAllTenants(ctx context.Context, migration *Migration) error
	// ApplyPending applies every migration file in MigrationsDir that has not
	// been recorded for the tenant, in filename order.
	ApplyPending(ctx context.Context, tenantID uuid.UUID) error
	// ApplyPendingToAllTenants runs ApplyPending for every active tenant.
	ApplyPendingToAllTenants(ctx context.Context) error
	GetAppliedMigrations(ctx context.Context, tenantID uuid.UUID) ([]*Migration, error)
	IsMigrationApplied(ctx context.Context, tenantID uuid.UUID, version string) (bool, error)
}
```

Add to `MockMigrationManager` in `test_helpers.go`:

```go
func (m *MockMigrationManager) ApplyPending(ctx context.Context, tenantID uuid.UUID) error { return nil }
func (m *MockMigrationManager) ApplyPendingToAllTenants(ctx context.Context) error       { return nil }
```

Add to `MockManagerMigrationManager` in `tenant/manager_test.go` (this mock is used by Task 3's provisioning tests, so give it a failure switch):

```go
// applyPendingErr, when set, is returned once by ApplyPending and then cleared.
func (m *MockManagerMigrationManager) ApplyPending(ctx context.Context, tenantID uuid.UUID) error {
	m.applyPendingCalls++
	if m.applyPendingErr != nil {
		err := m.applyPendingErr
		m.applyPendingErr = nil
		return err
	}
	return nil
}
func (m *MockManagerMigrationManager) ApplyPendingToAllTenants(ctx context.Context) error { return nil }
```

and add fields `applyPendingErr error` and `applyPendingCalls int` to the `MockManagerMigrationManager` struct.

In `multitenant.go` `New`, immediately after `setupDatabase` succeeds:

```go
	// Validate the migrations directory early so a typo is visible at startup.
	if dir := config.Database.MigrationsDir; dir == "" {
		logger.Warn("MigrationsDir is not set; newly provisioned tenants will have an empty schema")
	} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		db.Close()
		return nil, fmt.Errorf("migrations directory %q is not a directory: %w", dir, err)
	}
```

(add `"os"` to imports).

- [ ] **Step 4: Add a unit test for the `MigrationsDir` validation in `New`**

Append to `multitenant_test.go`:

```go
func TestNew_RejectsMissingMigrationsDir(t *testing.T) {
	config := tenant.DefaultConfig()
	config.Database.DSN = "postgres://postgres:postgres@localhost:5432/test_multitenant?sslmode=disable"
	config.Database.MigrationsDir = filepath.Join(t.TempDir(), "does-not-exist")

	_, err := New(config)
	if err == nil {
		t.Fatalf("expected error for missing migrations directory")
	}
	if !strings.Contains(err.Error(), "migrations directory") {
		t.Errorf("error should mention the migrations directory, got: %v", err)
	}
}
```

Add `"path/filepath"` and `"strings"` to that file's imports if missing. This test needs a reachable database only to get past `setupDatabase`; if `db.Ping` fails first the error is a database error, so guard: wrap the body in `if testing.Short() { t.Skip("needs database") }`.

- [ ] **Step 5: Write the failing integration test for `ApplyPending`**

Append to `database_integration_test.go`:

```go
func TestDatabase_ApplyPending_AppliesFilesInOrderAndIsIdempotent(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	_, mm, ids, dir := fileMigrationEnv(t, tdb, 1)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "002", "add_col", "ALTER TABLE things ADD COLUMN note TEXT", "")
	writeMigrationFiles(t, dir, "001", "create_things", "CREATE TABLE things (id SERIAL PRIMARY KEY)", "DROP TABLE things")

	if err := mm.ApplyPending(ctx, ids[0]); err != nil {
		t.Fatalf("ApplyPending: %v", err)
	}
	applied, _ := mm.GetAppliedMigrations(ctx, ids[0])
	if len(applied) != 2 || applied[0].Version != "001" || applied[1].Version != "002" {
		t.Fatalf("expected 001 then 002, got %+v", applied)
	}

	// Second run applies nothing new.
	if err := mm.ApplyPending(ctx, ids[0]); err != nil {
		t.Fatalf("second ApplyPending: %v", err)
	}
	if applied, _ = mm.GetAppliedMigrations(ctx, ids[0]); len(applied) != 2 {
		t.Errorf("re-run should not add records, got %d", len(applied))
	}

	// A file added later is picked up.
	writeMigrationFiles(t, dir, "003", "add_more", "ALTER TABLE things ADD COLUMN more TEXT", "")
	if err := mm.ApplyPending(ctx, ids[0]); err != nil {
		t.Fatalf("third ApplyPending: %v", err)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, ids[0], "003"); !ok {
		t.Errorf("003 should be applied")
	}
}

func TestDatabase_ApplyPendingToAllTenants_BringsOlderTenantUpToDate(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, mm, ids, dir := fileMigrationEnv(t, tdb, 2)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "001", "create_things", "CREATE TABLE things (id SERIAL PRIMARY KEY)", "")
	if err := mm.ApplyPending(ctx, ids[0]); err != nil { // only the first tenant is current
		t.Fatalf("ApplyPending: %v", err)
	}
	if err := mt.Manager.SuspendTenant(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	third := uuid.New()
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: third, Name: "third", Subdomain: "third-" + third.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, third); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTestData(tdb.db, []uuid.UUID{third}) })

	if err := mm.ApplyPendingToAllTenants(ctx); err != nil {
		t.Fatalf("ApplyPendingToAllTenants: %v", err)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, third, "001"); !ok {
		t.Errorf("active tenant should have been migrated")
	}
	if ok, _ := mm.IsMigrationApplied(ctx, ids[1], "001"); ok {
		t.Errorf("suspended tenant must not be migrated")
	}
}
```

`fileMigrationEnv` and `writeMigrationFiles` already exist in this file (added in PR #4).

- [ ] **Step 6: Run everything**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`. Note: `TestDatabase_ApplyPendingToAllTenants_BringsOlderTenantUpToDate` calls `ProvisionTenant`, which at this point still creates the hardcoded tables; that is fine here and changes in Task 3.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(migrations): sorted file discovery, ApplyPending, MigrationsDir validation

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: Test fixture migrations and shared test config

**Files:**
- Create: `testdata/migrations/001_create_projects.up.sql`, `001_create_projects.down.sql`, `002_create_tenant_users.up.sql`, `002_create_tenant_users.down.sql`
- Modify: `database_integration_test.go`, `integration_test.go` (every `tenant.DefaultConfig()` site)

**Interfaces:**
- Produces: `testConfig(dsn string) tenant.Config` in `database_integration_test.go`, used by every integration test that calls `New`.

This task changes no production code. Its purpose is that when Task 3 removes the hardcoded tables, every existing test keeps a `projects` and `tenant_users` table because provisioning now applies these files.

- [ ] **Step 1: Create the fixture**

`testdata/migrations/001_create_projects.up.sql`:

```sql
CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_projects_status ON projects(status);

CREATE FUNCTION update_updated_at_column() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_projects_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
```

`testdata/migrations/001_create_projects.down.sql`:

```sql
DROP TRIGGER IF EXISTS update_projects_updated_at ON projects;
DROP FUNCTION IF EXISTS update_updated_at_column();
DROP TABLE IF EXISTS projects;
```

`testdata/migrations/002_create_tenant_users.up.sql`:

```sql
CREATE TABLE tenant_users (
    user_id UUID PRIMARY KEY,
    role VARCHAR(50) NOT NULL DEFAULT 'user',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    joined_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

`testdata/migrations/002_create_tenant_users.down.sql`:

```sql
DROP TABLE IF EXISTS tenant_users;
```

- [ ] **Step 2: Add `testConfig` and use it everywhere**

Add near the top of `database_integration_test.go` (after `newTestDB`):

```go
// fixtureMigrationsDir is the schema every integration tenant gets.
var fixtureMigrationsDir = filepath.Join("testdata", "migrations")

// testConfig returns the config integration tests use: the fixture migrations
// and usage counting for the two fixture tables.
func testConfig(dsn string) tenant.Config {
	config := tenant.DefaultConfig()
	config.Database.DSN = dsn
	config.Database.MigrationsDir = fixtureMigrationsDir
	config.Limits.UsageTables = map[string]string{
		"max_projects": "projects",
		"max_users":    "tenant_users",
	}
	return config
}
```

`UsageTables` does not exist until Task 4. **For this task, omit the `config.Limits.UsageTables` lines**; Task 4 adds them back.

Replace every configuration site. In `database_integration_test.go` the pattern is two lines:

```go
	config := tenant.DefaultConfig()
	config.Database.DSN = connStr
```

Replace each with `config := testConfig(connStr)`. Run:

```bash
python3 - <<'EOF'
import re
p='database_integration_test.go'; s=open(p).read()
s, n = re.subn(r'config := tenant\.DefaultConfig\(\)\n(\t+)config\.Database\.DSN = connStr\n', r'config := testConfig(connStr)\n', s)
print("replaced", n)
open(p,'w').write(s)
EOF
```

Then find any remaining `tenant.DefaultConfig()` in that file (`grep -n 'DefaultConfig' database_integration_test.go`) and convert by hand (some sites assign `config.Database.MaxOpenConns` etc. afterwards; keep those lines). `fileMigrationEnv` must keep overriding `config.Database.MigrationsDir = dir` after calling `testConfig`.

In `integration_test.go` the pattern is `config.Database.DSN = getTestDatabaseURL()`; replace `config := tenant.DefaultConfig()` + that line with `config := testConfig(getTestDatabaseURL())`, and delete any `config.Database.MigrationsDir = ""` lines.

- [ ] **Step 3: Run the integration suite**

Run: `set -o pipefail; go vet ./... && go test -count=1 ./...`
Expected: all `ok`. At this point provisioning still creates the hardcoded tables and then applies the fixture on top; `CREATE TABLE projects` in the fixture would collide with the hardcoded table. **If you see `relation "projects" already exists`, that is expected and proves the fixture is applied**; proceed straight to Task 3 without committing, and commit Tasks 2 and 3 together. If everything passes (the hardcoded creation used `IF NOT EXISTS` and the fixture does not), commit now.

- [ ] **Step 4: Commit (if green)**

```bash
git add -A
git commit -m "test: fixture migrations and shared testConfig for integration tests

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Provision from migrations; remove hardcoded tables and deprecated DB access

**Files:**
- Modify: `database/schema.go` (delete `createTenantTables`, `SetSearchPath`; change `CreateTenantSchema`)
- Modify: `tenant/interfaces.go` (`SchemaManager`, `Manager`, context keys)
- Modify: `tenant/manager.go` (`ProvisionTenant`, delete `GetTenantDB`)
- Modify: `test_helpers.go`, `tenant/manager_test.go`, `multitenant_test.go` (mocks)
- Modify: `database/schema_test.go`, `database_integration_test.go`, `integration_test.go`
- Modify: `README.md` (only the `GetTenantDB` mentions, if any remain)

**Interfaces:**
- Consumes: `MigrationManager.ApplyPending` (Task 1), fixture (Task 2).
- Produces: `SchemaManager.CreateTenantSchema(ctx, tenantID) error` (no `name`); `Manager` without `GetTenantDB`; `ProvisionTenant` resumable.

- [ ] **Step 1: Write failing unit tests for the new provisioning behaviour**

In `tenant/manager_test.go`, replace `TestManager_ProvisionTenant` with:

```go
func TestManager_ProvisionTenant_CreatesSchemaAppliesPendingAndActivates(t *testing.T) {
	mockRepo, mockSchema, mockMig := newManagerMocks()
	manager := newTestManager(mockRepo, mockSchema, mockMig)
	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "ttt", Status: StatusPending}

	if err := manager.ProvisionTenant(context.Background(), tenantID); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if exists, _ := mockSchema.SchemaExists(context.Background(), tenantID); !exists {
		t.Error("schema should exist")
	}
	if mockMig.applyPendingCalls != 1 {
		t.Errorf("ApplyPending calls = %d, want 1", mockMig.applyPendingCalls)
	}
	if mockRepo.tenants[tenantID].Status != StatusActive {
		t.Errorf("status = %s, want active", mockRepo.tenants[tenantID].Status)
	}
}

func TestManager_ProvisionTenant_LeavesPendingOnMigrationFailureAndResumes(t *testing.T) {
	mockRepo, mockSchema, mockMig := newManagerMocks()
	manager := newTestManager(mockRepo, mockSchema, mockMig)
	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "ttt", Status: StatusPending}
	mockMig.applyPendingErr = errors.New("migration 002 failed")

	err := manager.ProvisionTenant(context.Background(), tenantID)
	if err == nil || !strings.Contains(err.Error(), "migration 002 failed") {
		t.Fatalf("expected migration error, got %v", err)
	}
	if mockRepo.tenants[tenantID].Status != StatusPending {
		t.Errorf("status after failure = %s, want pending", mockRepo.tenants[tenantID].Status)
	}
	if exists, _ := mockSchema.SchemaExists(context.Background(), tenantID); !exists {
		t.Error("schema should be kept for a resumable retry")
	}

	if err := manager.ProvisionTenant(context.Background(), tenantID); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if mockRepo.tenants[tenantID].Status != StatusActive {
		t.Errorf("status after retry = %s, want active", mockRepo.tenants[tenantID].Status)
	}
}

func TestManager_ProvisionTenant_RejectsCancelledTenant(t *testing.T) {
	mockRepo, mockSchema, mockMig := newManagerMocks()
	manager := newTestManager(mockRepo, mockSchema, mockMig)
	tenantID := uuid.New()
	mockRepo.tenants[tenantID] = &Tenant{ID: tenantID, Name: "T", Subdomain: "ttt", Status: StatusCancelled}

	if err := manager.ProvisionTenant(context.Background(), tenantID); err == nil {
		t.Fatal("expected error provisioning a cancelled tenant")
	}
}
```

Read the top of the existing `TestManager_ProvisionTenant` to see how it constructs mocks and the manager; if helpers named `newManagerMocks`/`newTestManager` do not exist, add them next to the mocks so the three tests above compile:

```go
func newManagerMocks() (*MockManagerRepository, *MockManagerSchemaManager, *MockManagerMigrationManager) {
	return &MockManagerRepository{tenants: make(map[uuid.UUID]*Tenant)},
		&MockManagerSchemaManager{schemas: make(map[uuid.UUID]bool)},
		&MockManagerMigrationManager{}
}

func newTestManager(repo *MockManagerRepository, schema *MockManagerSchemaManager, mig *MockManagerMigrationManager) Manager {
	return NewManager(DefaultConfig(), nil, repo, schema, mig, &MockManagerLimitChecker{}, zap.NewNop())
}
```

Adapt field names to whatever the existing mocks use (`grep -n 'type MockManagerSchemaManager' -A 5 tenant/manager_test.go`).

- [ ] **Step 2: Run them and confirm failure**

Run: `go test -count=1 -run 'TestManager_ProvisionTenant' ./tenant/`
Expected: compile failure on `applyPendingCalls` if Task 1's mock fields were not added, otherwise FAIL: `ApplyPending calls = 0, want 1` and the resume test failing because the old code returns early when the schema exists.

- [ ] **Step 3: Implement**

`database/schema.go`: delete `SetSearchPath` and `createTenantTables`. Replace `CreateTenantSchema` with:

```go
// CreateTenantSchema creates the tenant's schema. It creates no tables; the
// application's migration files define the schema contents.
func (sm *SchemaManager) CreateTenantSchema(ctx context.Context, tenantID uuid.UUID) error {
	schemaName := sm.GetSchemaName(tenantID)
	sm.logger.Info("Creating tenant schema",
		zap.String("tenant_id", tenantID.String()),
		zap.String("schema_name", schemaName))

	createSchemaSQL := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", sm.quotedSchemaName(tenantID))
	if _, err := sm.db.ExecContext(ctx, createSchemaSQL); err != nil {
		return fmt.Errorf("failed to create schema %s: %w", schemaName, err)
	}
	return nil
}
```

Remove the `"time"` import only if nothing else uses it (`SchemaExists` uses `time` for its timeout; keep it).

`tenant/interfaces.go`: `SchemaManager` becomes

```go
type SchemaManager interface {
	CreateTenantSchema(ctx context.Context, tenantID uuid.UUID) error
	DropTenantSchema(ctx context.Context, tenantID uuid.UUID) error
	SchemaExists(ctx context.Context, tenantID uuid.UUID) (bool, error)
	GetSchemaName(tenantID uuid.UUID) string
	ListTenantSchemas(ctx context.Context) ([]string, error)
}
```

Delete from the `Manager` interface the `GetTenantDB` method and its comment. Delete `ContextKeyTenantDB` and `GetTenantDBFromContext`. Remove the `"database/sql"` import from `interfaces.go` only if `*sql.Tx` in `WithTenantTx` no longer needs it (it does; keep).

`tenant/manager.go`: delete `GetTenantDB`. Replace `ProvisionTenant`:

```go
// ProvisionTenant creates the tenant schema, applies every pending migration
// file, and activates the tenant. It is safe to re-run: a failed provision
// leaves the tenant pending with the work done so far, and the next run
// continues from there.
func (m *manager) ProvisionTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}
	if tenant.Status == StatusCancelled {
		return fmt.Errorf("cannot provision cancelled tenant %s", id)
	}

	if err := m.schemaManager.CreateTenantSchema(ctx, id); err != nil {
		return fmt.Errorf("failed to create tenant schema: %w", err)
	}

	if err := m.migrationMgr.ApplyPending(ctx, id); err != nil {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	if tenant.Status == StatusActive {
		return nil
	}
	tenant.Status = StatusActive
	if err := m.repository.Update(ctx, tenant); err != nil {
		return fmt.Errorf("failed to activate tenant: %w", err)
	}

	m.logger.Info("Successfully provisioned tenant",
		zap.String("tenant_id", id.String()),
		zap.String("name", tenant.Name))
	return nil
}
```

Mocks:
- `test_helpers.go` `MockSchemaManager`: change `CreateTenantSchema(ctx, tenantID, name)` to `CreateTenantSchema(ctx, tenantID)`; delete `SetSearchPath`.
- `tenant/manager_test.go` `MockManagerSchemaManager`: same two changes.
- `multitenant_test.go` `MockMultiTenantManager`: delete `GetTenantDB`.
- `database/schema_test.go`: `TestSchemaManager_Implementation` lists interface methods; drop `SetSearchPath` and fix the `CreateTenantSchema` signature there.

Integration tests. The four schema-object tests now verify objects created by the fixture migration instead of hardcoded ones:
- `TestDatabase_SchemaCreation_TablesOnlyInTenantSchema`: expected tenant tables become `[]string{"projects", "tenant_users"}`.
- `TestDatabase_SchemaCreation_IndexesInCorrectSchema`: expected index `idx_projects_status` only.
- `TestDatabase_SchemaCreation_FunctionsInCorrectSchema`: function `update_updated_at_column` in tenant schema, absent from public.
- `TestDatabase_SchemaCreation_TriggersWork`: trigger `update_projects_updated_at` on `projects`.
Any test that inserted into `tasks` or `documents` switches to `projects`. Any direct call `sm.CreateTenantSchema(ctx, id, name)` drops the name argument; tests that called it directly (not through `ProvisionTenant`) and then expected tables must instead go through `mt.Manager.ProvisionTenant` or call `mt.Migrations.ApplyPending(ctx, id)` after creating the schema.

Add two integration tests:

```go
func TestDatabase_Provision_EmptyMigrationsDirYieldsEmptySchema(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	config := testConfig(connStr)
	config.Database.MigrationsDir = ""
	mt, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer mt.Close()
	ctx := context.Background()
	id := uuid.New()
	defer cleanupTestData(tdb.db, []uuid.UUID{id})
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "empty", Subdomain: "empty-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	schema := fmt.Sprintf("tenant_%s", strings.ReplaceAll(id.String(), "-", "_"))
	tables, _ := tdb.listTablesInSchema(schema)
	if len(tables) != 0 {
		t.Errorf("expected no tables, got %v", tables)
	}
	got, _ := mt.Manager.GetTenant(ctx, id)
	if got.Status != tenant.StatusActive {
		t.Errorf("status = %s, want active", got.Status)
	}
}

func TestDatabase_Provision_ResumesAfterFailingMigration(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, mm, _, dir := fileMigrationEnv(t, tdb, 0)
	ctx := context.Background()
	writeMigrationFiles(t, dir, "001", "ok", "CREATE TABLE ok_table (id INT)", "")
	writeMigrationFiles(t, dir, "002", "broken", "CREATE TABLE (", "")

	id := uuid.New()
	t.Cleanup(func() { cleanupTestData(tdb.db, []uuid.UUID{id}) })
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "r", Subdomain: "resume-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}

	err := mt.Manager.ProvisionTenant(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "002") {
		t.Fatalf("expected failure naming version 002, got %v", err)
	}
	got, _ := mt.Manager.GetTenant(ctx, id)
	if got.Status != tenant.StatusPending {
		t.Errorf("status after failure = %s, want pending", got.Status)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, id, "001"); !ok {
		t.Errorf("001 should remain applied")
	}

	// Fix the migration and retry.
	writeMigrationFiles(t, dir, "002", "broken", "CREATE TABLE fixed_table (id INT)", "")
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got, _ = mt.Manager.GetTenant(ctx, id)
	if got.Status != tenant.StatusActive {
		t.Errorf("status after retry = %s, want active", got.Status)
	}
	if ok, _ := mm.IsMigrationApplied(ctx, id, "002"); !ok {
		t.Errorf("002 should be applied after retry")
	}
}
```

Also grep the repo for `GetTenantDB` and `SetSearchPath` (`grep -rn 'GetTenantDB\|SetSearchPath' --include='*.go' --include='*.md' .`) and remove every remaining reference, including in `README.md` and `tenant/interfaces.go` comments.

- [ ] **Step 4: Run everything**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat!: provision tenants from migration files; remove hardcoded tables

CreateTenantSchema only creates the schema. ProvisionTenant applies
pending migration files and activates; it is resumable after a failed
migration and rejects cancelled tenants. Removes the deprecated
GetTenantDB, GetTenantDBFromContext, ContextKeyTenantDB and
SchemaManager.SetSearchPath.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: `UsageTables`, configurable usage tracker, and `Manager.GetStats`

**Files:**
- Modify: `tenant/models.go` (`LimitsConfig.UsageTables`, `Stats`)
- Modify: `database/postgres/usage_tracker.go`
- Modify: `database/postgres/repository.go` (delete `GetStats`)
- Modify: `tenant/interfaces.go` (`Repository` without `GetStats`)
- Modify: `tenant/manager.go` (`GetStats`)
- Modify: `multitenant.go` (tracker wiring)
- Modify: mocks in `test_helpers.go`, `tenant/manager_test.go`, `tenant/resolver_test.go`, `tenant/limit_checker_test.go`, `multitenant_test.go`
- Modify: `tenant/models_test.go` (`TestStats_Fields`), `examples/with-billing/main.go`
- Test: `database/postgres/usage_tracker_test.go` (new), `database_integration_test.go`

**Interfaces:**
- Produces: `postgres.NewUsageTracker(db *sql.DB, sm tenant.SchemaManager, usageTables map[string]string, logger *zap.Logger) (*UsageTracker, error)`; `tenant.Stats{TenantID, SchemaExists, AppliedMigrations, Usage}`; `Manager.GetStats` implemented on the manager.

- [ ] **Step 1: Write failing unit tests for the tracker**

Create `database/postgres/usage_tracker_test.go`:

```go
package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeSchemaManager struct{}

func (fakeSchemaManager) CreateTenantSchema(ctx context.Context, id uuid.UUID) error { return nil }
func (fakeSchemaManager) DropTenantSchema(ctx context.Context, id uuid.UUID) error   { return nil }
func (fakeSchemaManager) SchemaExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return true, nil
}
func (fakeSchemaManager) GetSchemaName(id uuid.UUID) string                    { return "tenant_x" }
func (fakeSchemaManager) ListTenantSchemas(ctx context.Context) ([]string, error) { return nil, nil }

func TestNewUsageTracker_RejectsUnsafeTableNames(t *testing.T) {
	for _, bad := range []string{"projects; drop table x", "Projects", "1abc", "a-b", ""} {
		_, err := NewUsageTracker(nil, fakeSchemaManager{}, map[string]string{"max_x": bad}, zap.NewNop())
		if err == nil {
			t.Errorf("table name %q should be rejected", bad)
		}
	}
}

func TestUsageTracker_UnknownLimitReturnsNil(t *testing.T) {
	tr, err := NewUsageTracker(nil, fakeSchemaManager{}, map[string]string{"max_projects": "projects"}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	v, err := tr.GetCurrentUsage(context.Background(), uuid.New(), "max_storage_gb")
	if err != nil || v != nil {
		t.Errorf("got %v, %v; want nil, nil", v, err)
	}
}
```

- [ ] **Step 2: Run and confirm compile failure**

Run: `go test -count=1 ./database/postgres/`
Expected: compile error, `NewUsageTracker` has the old signature.

- [ ] **Step 3: Implement the tracker, `Stats`, `UsageTables`, and `Manager.GetStats`**

`tenant/models.go`:

```go
// Stats represents usage statistics for a tenant
type Stats struct {
	TenantID          uuid.UUID      `json:"tenant_id"`
	SchemaExists      bool           `json:"schema_exists"`
	AppliedMigrations int            `json:"applied_migrations"`
	// Usage holds the current count for each limit named in LimitsConfig.UsageTables.
	Usage map[string]int `json:"usage"`
}
```

and in `LimitsConfig` add:

```go
	// UsageTables maps a limit name to a table in the tenant schema whose row
	// count is that limit's current usage, e.g. {"max_projects": "projects"}.
	// Limits not listed here are not checked unless a custom UsageTracker
	// supplies a value.
	UsageTables map[string]string `json:"usage_tables"`
```

`DefaultConfig()` leaves `UsageTables` nil. Remove the now-unused `"time"` import from `models.go` only if nothing else uses it (`DatabaseConfig` uses `time.Duration`; keep).

`database/postgres/usage_tracker.go`, full replacement:

```go
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// tableNamePattern is the only shape of table name the tracker will interpolate.
var tableNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// UsageTracker implements tenant.UsageTracker by counting rows in tables of
// the tenant schema. Which table backs which limit comes from
// LimitsConfig.UsageTables; limits not listed report nil and are not checked.
//
// Usage is derived from the tables themselves, so Increment, Decrement and
// Reset are no-ops.
type UsageTracker struct {
	db            *sql.DB
	schemaManager tenant.SchemaManager
	usageTables   map[string]string
	logger        *zap.Logger
}

var _ tenant.UsageTracker = (*UsageTracker)(nil)

// NewUsageTracker creates a tracker for the given limit-to-table map. Every
// table name must match ^[a-z_][a-z0-9_]*$.
func NewUsageTracker(db *sql.DB, schemaManager tenant.SchemaManager, usageTables map[string]string, logger *zap.Logger) (*UsageTracker, error) {
	tables := make(map[string]string, len(usageTables))
	for limit, table := range usageTables {
		if !tableNamePattern.MatchString(table) {
			return nil, fmt.Errorf("usage table for limit %q: %q is not a valid table name", limit, table)
		}
		tables[limit] = table
	}
	return &UsageTracker{
		db:            db,
		schemaManager: schemaManager,
		usageTables:   tables,
		logger:        logger.Named("usage_tracker"),
	}, nil
}

// GetCurrentUsage returns the row count of the table mapped to limitName, or
// nil if the limit is not mapped.
func (u *UsageTracker) GetCurrentUsage(ctx context.Context, tenantID uuid.UUID, limitName string) (interface{}, error) {
	table, ok := u.usageTables[limitName]
	if !ok {
		return nil, nil
	}
	schema := u.schemaManager.GetSchemaName(tenantID)
	query := fmt.Sprintf(`SELECT COUNT(*) FROM "%s".%s`, schema, table)

	var n int
	if err := u.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return nil, fmt.Errorf("failed to count %s for tenant %s: %w", table, tenantID, err)
	}
	return n, nil
}

// IncrementUsage is a no-op: usage is derived from the tenant's tables.
func (u *UsageTracker) IncrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error {
	return nil
}

// DecrementUsage is a no-op: usage is derived from the tenant's tables.
func (u *UsageTracker) DecrementUsage(ctx context.Context, tenantID uuid.UUID, limitName string, delta interface{}) error {
	return nil
}

// ResetUsage is a no-op: usage is derived from the tenant's tables.
func (u *UsageTracker) ResetUsage(ctx context.Context, tenantID uuid.UUID, limitName string) error {
	return nil
}
```

`multitenant.go` wiring:

```go
	limitChecker := tenant.NewLimitChecker(config.Limits, repository, logger)
	usageTracker, err := postgres.NewUsageTracker(db, schemaManager, config.Limits.UsageTables, logger)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to configure usage tracker: %w", err)
	}
	limitChecker.SetUsageTracker(usageTracker)
```

`tenant/interfaces.go`: delete `GetStats` from `Repository`. `database/postgres/repository.go`: delete `GetStats` and the now-unused `"time"` import if any (it is still used by `Create`/`Update`; keep). Delete `GetStats` from every repository mock: `test_helpers.go` `MockRepository`, `tenant/manager_test.go` `MockManagerRepository`, `tenant/resolver_test.go` `mockRepository`, `tenant/limit_checker_test.go` `MockLimitCheckerRepository`.

`tenant/manager.go`, replace `GetStats`:

```go
// GetStats reports whether the schema exists, how many migrations are applied,
// and the current usage for every limit listed in LimitsConfig.UsageTables.
func (m *manager) GetStats(ctx context.Context, tenantID uuid.UUID) (*Stats, error) {
	if _, err := m.repository.GetByID(ctx, tenantID); err != nil {
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}
	exists, err := m.schemaManager.SchemaExists(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to check schema: %w", err)
	}
	stats := &Stats{TenantID: tenantID, SchemaExists: exists, Usage: make(map[string]int)}
	if !exists {
		return stats, nil
	}

	applied, err := m.migrationMgr.GetAppliedMigrations(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to list applied migrations: %w", err)
	}
	stats.AppliedMigrations = len(applied)

	tracker := m.limitChecker.GetUsageTracker()
	if tracker == nil {
		return stats, nil
	}
	for limitName := range m.config.Limits.UsageTables {
		value, err := tracker.GetCurrentUsage(ctx, tenantID, limitName)
		if err != nil {
			m.logger.Warn("Failed to read usage",
				zap.String("tenant_id", tenantID.String()), zap.String("limit", limitName), zap.Error(err))
			continue
		}
		if n, ok := value.(int); ok {
			stats.Usage[limitName] = n
		}
	}
	return stats, nil
}
```

`tenant/manager_test.go` `TestManager_GetStats`: rewrite to assert `SchemaExists` from the schema mock and `AppliedMigrations` from a migration mock that returns two migrations from `GetAppliedMigrations` (add a `applied []*Migration` field to `MockManagerMigrationManager` and return it). `MockManagerLimitChecker.GetUsageTracker` returns nil, so `Usage` is empty; assert `len(stats.Usage) == 0`.

`tenant/models_test.go` `TestStats_Fields`: construct `Stats{TenantID: id, SchemaExists: true, AppliedMigrations: 2, Usage: map[string]int{"max_projects": 3}}` and assert each field.

`test_helpers.go` `MockRepository.GetStats` referenced `UserCount` etc.; it is deleted with the method.

`examples/with-billing/main.go`:
- In `createProject`, replace the `stats.ProjectCount >= limits.MaxProjects` check with `stats.Usage["max_projects"] >= limits.MaxProjects`.
- In `getUsage`, replace the `current` map with `"projects": stats.Usage["max_projects"], "users": stats.Usage["max_users"]`, drop the storage line, and compute percentages from those two. Delete `calculatePercentage`'s storage call.
- In `main`, after building `config`, add `config.Limits.UsageTables = map[string]string{"max_projects": "projects", "max_users": "tenant_users"}` and set `config.Database.MigrationsDir = "./migrations"` with a comment that the app's migration files define those tables.
- Any other `stats.UserCount`/`ProjectCount`/`StorageUsedGB` use (around line 470) becomes `stats.Usage[...]`.

Now re-add to `testConfig` in `database_integration_test.go` the lines Task 2 omitted:

```go
	config.Limits.UsageTables = map[string]string{
		"max_projects": "projects",
		"max_users":    "tenant_users",
	}
```

- [ ] **Step 4: Write the integration tests**

Append to `database_integration_test.go`:

```go
func TestDatabase_UsageTracker_CountsConfiguredTableAndSkipsOthers(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	seedProjects(t, mt, ids[0], 3)

	tracker := mt.Manager.LimitChecker().GetUsageTracker()
	v, err := tracker.GetCurrentUsage(ctx, ids[0], "max_projects")
	if err != nil || v != 3 {
		t.Errorf("max_projects usage = %v, %v; want 3", v, err)
	}
	v, err = tracker.GetCurrentUsage(ctx, ids[0], "api_calls_per_month")
	if err != nil || v != nil {
		t.Errorf("unmapped limit should report nil, got %v, %v", v, err)
	}
}

func TestDatabase_GetStats_ReportsMigrationsAndUsage(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 1)
	ctx := context.Background()
	seedProjects(t, mt, ids[0], 2)

	stats, err := mt.Manager.GetStats(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if !stats.SchemaExists {
		t.Error("SchemaExists should be true")
	}
	if stats.AppliedMigrations != 2 { // fixture has 001 and 002
		t.Errorf("AppliedMigrations = %d, want 2", stats.AppliedMigrations)
	}
	if stats.Usage["max_projects"] != 2 || stats.Usage["max_users"] != 0 {
		t.Errorf("Usage = %v, want max_projects=2 max_users=0", stats.Usage)
	}
}
```

`migrationTestEnv` and `seedProjects` already exist in this file.

- [ ] **Step 5: Run everything**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`. The existing `TestDatabase_Limits_*` tests keep passing because `testConfig` now maps `max_projects`.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat!: usage tracking from LimitsConfig.UsageTables; GetStats on Manager

The Postgres usage tracker counts only tables the application names in
UsageTables. Stats now reports schema existence, applied migration
count and per-limit usage; Repository.GetStats is removed.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Metadata on the core `Tenant`; delete the extensible layer

**Files:**
- Rename: `tenant/extensible_models.go` → `tenant/metadata.go` (keep `TenantMetadata`, Stripe/Branding wrappers; delete `ExtensibleTenant`, `ToBaseTenant`, `FromBaseTenant`)
- Delete: `tenant/extensible_interfaces.go`, `database/postgres/extensible_repository.go`, `examples/extensible-tenant/`
- Modify: `tenant/models.go` (`Tenant.Metadata`), `tenant/interfaces.go` (`Repository.FindByMetadata`), `database/postgres/repository.go`, mocks (4 repository mocks), `multitenant.go` (re-exports)
- Test: `tenant/metadata_test.go` (new), `database_integration_test.go`

**Interfaces:**
- Produces: `Tenant.Metadata TenantMetadata`; `Repository.FindByMetadata(ctx, key, value string) ([]*Tenant, error)`; re-exports `multitenant.TenantMetadata`, `multitenant.NewStripeExtension`, `multitenant.NewBrandingExtension`.

- [ ] **Step 1: Write failing unit test for the metadata round trip**

Create `tenant/metadata_test.go`:

```go
package tenant

import "testing"

func TestTenantMetadata_ValueScanRoundTrip(t *testing.T) {
	in := TenantMetadata{"stripe_customer_id": "cus_123", "seats": 5, "beta": true}

	v, err := in.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	var out TenantMetadata
	if err := out.Scan(v); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if s, _ := out.GetString("stripe_customer_id"); s != "cus_123" {
		t.Errorf("stripe_customer_id = %q", s)
	}
	if n, _ := out.GetInt("seats"); n != 5 {
		t.Errorf("seats = %d", n)
	}
	if b, _ := out.GetBool("beta"); !b {
		t.Errorf("beta should be true")
	}
}

func TestTenantMetadata_ScanNilYieldsEmptyMap(t *testing.T) {
	var out TenantMetadata
	if err := out.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if out == nil {
		t.Fatal("Scan(nil) should leave a non-nil empty map")
	}
}

func TestTenantMetadata_NilValueIsEmptyObject(t *testing.T) {
	var m TenantMetadata
	v, err := m.Value()
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := v.(string); !ok || s != "{}" {
		if b, ok := v.([]byte); !ok || string(b) != "{}" {
			t.Errorf("nil metadata should serialize as {}, got %v", v)
		}
	}
}

func TestTenant_HasMetadataField(t *testing.T) {
	tn := Tenant{Metadata: TenantMetadata{"k": "v"}}
	if s, _ := tn.Metadata.GetString("k"); s != "v" {
		t.Errorf("Tenant.Metadata not wired")
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test -count=1 -run 'TestTenantMetadata|TestTenant_HasMetadataField' ./tenant/`
Expected: compile error `Tenant has no field Metadata`; after temporarily commenting that test, `ScanNilYieldsEmptyMap` or `NilValueIsEmptyObject` may FAIL depending on the current `Scan`/`Value` implementation. Note what fails.

- [ ] **Step 3: Implement**

```bash
git mv tenant/extensible_models.go tenant/metadata.go
git rm -q tenant/extensible_interfaces.go database/postgres/extensible_repository.go
git rm -q -r examples/extensible-tenant
```

In `tenant/metadata.go`: delete the `ExtensibleTenant` type, `ToBaseTenant`, `FromBaseTenant`. Make `Value` return `[]byte("{}")` when the map is nil, and make `Scan` set `*tm = TenantMetadata{}` when `value == nil`. Keep every other method unchanged. Update the file's package comment to describe per-tenant metadata.

`tenant/models.go`, add to `Tenant`:

```go
	Metadata   TenantMetadata `json:"metadata"`
```

`tenant/interfaces.go`, add to `Repository`:

```go
	// FindByMetadata returns tenants whose metadata[key] equals value (as text).
	FindByMetadata(ctx context.Context, key, value string) ([]*Tenant, error)
```

`database/postgres/repository.go`:
- `Create`: columns `(id, name, subdomain, plan_type, status, schema_name, metadata, created_at, updated_at)` with `$7 = t.Metadata` (pass the map; its `Value` method serializes it). Shift `created_at`/`updated_at` to `$8`, `$9`.
- `GetByID`, `GetBySubdomain`, `List`: add `metadata` to the SELECT list right after `schema_name` and scan into `&t.Metadata`.
- `Update`: `SET name = $2, subdomain = $3, plan_type = $4, status = $5, metadata = $6, updated_at = $7`.
- Add:

```go
// FindByMetadata returns tenants whose metadata[key] equals value.
func (r *Repository) FindByMetadata(ctx context.Context, key, value string) ([]*tenant.Tenant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, subdomain, plan_type, status, schema_name, metadata, created_at, updated_at
		FROM public.tenants
		WHERE metadata ->> $1 = $2
		ORDER BY created_at`, key, value)
	if err != nil {
		return nil, fmt.Errorf("failed to query tenants by metadata: %w", err)
	}
	defer rows.Close()
	return scanTenants(rows)
}

// scanTenants reads every row of a tenants SELECT with the standard column order.
func scanTenants(rows *sql.Rows) ([]*tenant.Tenant, error) {
	var tenants []*tenant.Tenant
	for rows.Next() {
		t := &tenant.Tenant{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Subdomain, &t.PlanType, &t.Status, &t.SchemaName, &t.Metadata, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}
```

and make `List` use `scanTenants` too (replacing its manual loop, which silently skipped rows on scan error).

- `CreateMasterTables`: add `metadata JSONB NOT NULL DEFAULT '{}'` to the `CREATE TABLE public.tenants` column list, and append to `tables` (it is executed in order):

```go
		`ALTER TABLE public.tenants ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'`,
```

and to `indexes`:

```go
		"CREATE INDEX IF NOT EXISTS idx_tenants_metadata ON public.tenants USING GIN (metadata)",
```

Mocks: add `FindByMetadata` to `MockRepository` (`test_helpers.go`), `MockManagerRepository`, `mockRepository` (`resolver_test.go`), `MockLimitCheckerRepository`. A faithful in-memory version for the two map-backed mocks:

```go
func (m *MockRepository) FindByMetadata(ctx context.Context, key, value string) ([]*tenant.Tenant, error) {
	var out []*tenant.Tenant
	for _, t := range m.tenants {
		if s, ok := t.Metadata.GetString(key); ok && s == value {
			out = append(out, t)
		}
	}
	return out, nil
}
```

(The other two can return `nil, nil`.)

`multitenant.go` re-exports: add `TenantMetadata = tenant.TenantMetadata` to the type block and `NewStripeExtension = tenant.NewStripeExtension`, `NewBrandingExtension = tenant.NewBrandingExtension` to the var block.

Delete `examples/stripe-integration/README.md` (Task 7 replaces it with code). Remove the `EXTENSIBILITY_GUIDE.md` content down to a one-line stub "Rewritten in Task 8" only if you would otherwise leave references to deleted types; otherwise leave it for Task 8.

- [ ] **Step 4: Write the integration tests**

Append to `database_integration_test.go`:

```go
func TestDatabase_Metadata_RoundTripsThroughRepositoryAndFindByMetadata(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, ids := migrationTestEnv(t, tdb, 2)
	ctx := context.Background()

	first, _ := mt.Manager.GetTenant(ctx, ids[0])
	if first.Metadata == nil {
		t.Fatal("Metadata should never be nil after a read")
	}
	tenant.NewStripeExtension(first.Metadata).SetCustomerID("cus_abc")
	first.Metadata.SetInt("seats", 7)
	if err := mt.Manager.UpdateTenant(ctx, first); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}

	got, _ := mt.Manager.GetTenant(ctx, ids[0])
	if id, _ := tenant.NewStripeExtension(got.Metadata).GetCustomerID(); id != "cus_abc" {
		t.Errorf("customer id = %q", id)
	}
	if n, _ := got.Metadata.GetInt("seats"); n != 7 {
		t.Errorf("seats = %d", n)
	}
	bySub, _ := mt.Manager.GetTenantBySubdomain(ctx, got.Subdomain)
	if bySub.Metadata["seats"] == nil {
		t.Errorf("GetBySubdomain should load metadata")
	}
	list, _, _ := mt.Manager.ListTenants(ctx, 1, 100)
	found := false
	for _, lt := range list {
		if lt.ID == ids[0] && lt.Metadata["seats"] != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("List should load metadata")
	}

	repo := postgres.NewRepository(mt.GetDatabase(), mt.GetLogger())
	matches, err := repo.FindByMetadata(ctx, "stripe_customer_id", "cus_abc")
	if err != nil || len(matches) != 1 || matches[0].ID != ids[0] {
		t.Errorf("FindByMetadata = %v, %v; want only %s", matches, err, ids[0])
	}
}

func TestDatabase_Metadata_ColumnIsAddedToPreExistingTenantsTable(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	connStr := tdb.getConnectionString()
	if connStr == "" {
		t.Skip("No connection string available")
	}
	// Simulate a database created by an older version: no metadata column.
	if _, err := tdb.db.Exec(`DROP TABLE IF EXISTS public.tenant_migrations; DROP TABLE IF EXISTS public.tenants`); err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.db.Exec(`CREATE TABLE public.tenants (
		id UUID PRIMARY KEY, name VARCHAR(255) NOT NULL, subdomain VARCHAR(255) UNIQUE NOT NULL,
		plan_type VARCHAR(50) NOT NULL DEFAULT 'basic', status VARCHAR(50) NOT NULL DEFAULT 'pending',
		schema_name VARCHAR(255) NOT NULL,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	mt, err := New(testConfig(connStr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer mt.Close()

	var exists bool
	err = tdb.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name='tenants' AND column_name='metadata')`).Scan(&exists)
	if err != nil || !exists {
		t.Errorf("metadata column should have been added, exists=%v err=%v", exists, err)
	}
}
```

Add `"github.com/alexalmadav/go-multitenant/database/postgres"` to the file's imports. **Important:** the second test drops the shared `tenants` table. `cleanupTestData` already deletes all rows between tests, and `New` recreates tables, so subsequent tests are unaffected; but never run this package with `-parallel` on a shared database.

- [ ] **Step 5: Run everything**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`. `grep -rn 'ExtensibleTenant\|ExtensibleRepository\|ExtensionRegistry\|SchemaRegistry' --include='*.go' .` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat!: metadata JSONB on the core Tenant; remove the extensible layer

Tenant gains Metadata, stored in public.tenants.metadata (added with
ADD COLUMN IF NOT EXISTS for existing databases, GIN indexed) and read
and written by every Repository path. Repository gains FindByMetadata.
ExtensibleTenant, ExtensibleRepository and the unimplemented extension
interfaces are deleted along with their example.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Lifecycle hooks

**Files:**
- Create: `tenant/hooks.go`, `tenant/hooks_test.go`
- Modify: `tenant/interfaces.go` (`Manager.RegisterHook`), `tenant/manager.go`, `multitenant.go` (re-exports), `multitenant_test.go` (`MockMultiTenantManager.RegisterHook`)
- Test: `database_integration_test.go`

**Interfaces:**
- Produces:

```go
type Hook interface {
	Name() string
	ValidateMetadata(ctx context.Context, t *Tenant) error
	OnTenantCreated(ctx context.Context, t *Tenant) error
	OnTenantProvisioned(ctx context.Context, t *Tenant) error
	OnTenantUpdated(ctx context.Context, before, after *Tenant) error
	OnTenantStatusChanged(ctx context.Context, t *Tenant, previousStatus string) error
	OnTenantDeleted(ctx context.Context, t *Tenant) error
}
type BaseHook struct{}
type HookError struct { Event string; Errors []error }
// on Manager:
RegisterHook(h Hook)
```

- [ ] **Step 1: Write failing unit tests**

Create `tenant/hooks_test.go`:

```go
package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// recordingHook appends "<name>:<event>" to *log and returns fail for the events in failOn.
type recordingHook struct {
	BaseHook
	name   string
	log    *[]string
	failOn map[string]bool
}

func (h *recordingHook) Name() string { return h.name }
func (h *recordingHook) record(event string) error {
	*h.log = append(*h.log, h.name+":"+event)
	if h.failOn[event] {
		return errors.New(h.name + " failed " + event)
	}
	return nil
}
func (h *recordingHook) ValidateMetadata(ctx context.Context, t *Tenant) error { return h.record("validate") }
func (h *recordingHook) OnTenantCreated(ctx context.Context, t *Tenant) error  { return h.record("created") }
func (h *recordingHook) OnTenantProvisioned(ctx context.Context, t *Tenant) error {
	return h.record("provisioned")
}
func (h *recordingHook) OnTenantUpdated(ctx context.Context, before, after *Tenant) error {
	return h.record("updated")
}
func (h *recordingHook) OnTenantStatusChanged(ctx context.Context, t *Tenant, prev string) error {
	return h.record("status:" + prev + "->" + t.Status)
}
func (h *recordingHook) OnTenantDeleted(ctx context.Context, t *Tenant) error { return h.record("deleted") }

func hookedManager(t *testing.T, hooks ...Hook) (Manager, *MockManagerRepository) {
	t.Helper()
	repo, schema, mig := newManagerMocks()
	m := NewManager(DefaultConfig(), nil, repo, schema, mig, &MockManagerLimitChecker{}, zap.NewNop())
	for _, h := range hooks {
		m.RegisterHook(h)
	}
	return m, repo
}

func TestHooks_RunInRegistrationOrderOnCreate(t *testing.T) {
	var log []string
	m, _ := hookedManager(t, &recordingHook{name: "a", log: &log}, &recordingHook{name: "b", log: &log})

	err := m.CreateTenant(context.Background(), &Tenant{Name: "T", Subdomain: "ttt"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a:validate", "b:validate", "a:created", "b:created"}
	if strings.Join(log, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", log, want)
	}
}

func TestHooks_ValidateMetadataBlocksWrite(t *testing.T) {
	var log []string
	m, repo := hookedManager(t, &recordingHook{name: "a", log: &log, failOn: map[string]bool{"validate": true}})

	err := m.CreateTenant(context.Background(), &Tenant{Name: "T", Subdomain: "ttt"})
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "metadata" {
		t.Fatalf("want ValidationError on metadata, got %v", err)
	}
	if len(repo.tenants) != 0 {
		t.Error("tenant must not be written when validation fails")
	}
	for _, e := range log {
		if e == "a:created" {
			t.Error("OnTenantCreated must not run after failed validation")
		}
	}
}

func TestHooks_AfterWriteFailuresAreCollectedAndTenantPersists(t *testing.T) {
	var log []string
	m, repo := hookedManager(t,
		&recordingHook{name: "a", log: &log, failOn: map[string]bool{"created": true}},
		&recordingHook{name: "b", log: &log, failOn: map[string]bool{"created": true}},
		&recordingHook{name: "c", log: &log})

	err := m.CreateTenant(context.Background(), &Tenant{Name: "T", Subdomain: "ttt"})
	var herr *HookError
	if !errors.As(err, &herr) {
		t.Fatalf("want HookError, got %v", err)
	}
	if herr.Event != "created" || len(herr.Errors) != 2 {
		t.Errorf("HookError = %+v", herr)
	}
	if !strings.Contains(err.Error(), "a failed created") || !strings.Contains(err.Error(), "b failed created") {
		t.Errorf("error should name both failing hooks: %v", err)
	}
	if len(repo.tenants) != 1 {
		t.Error("tenant should persist despite hook failure")
	}
	if log[len(log)-1] != "c:created" {
		t.Errorf("every hook should still run; log = %v", log)
	}
}

func TestHooks_LifecycleEvents(t *testing.T) {
	var log []string
	m, repo := hookedManager(t, &recordingHook{name: "h", log: &log})
	ctx := context.Background()
	tn := &Tenant{Name: "T", Subdomain: "ttt"}
	if err := m.CreateTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	log = nil

	if err := m.ProvisionTenant(ctx, tn.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ProvisionTenant(ctx, tn.ID); err != nil { // re-run: no events
		t.Fatal(err)
	}
	tn.Name = "Renamed"
	if err := m.UpdateTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	if err := m.SuspendTenant(ctx, tn.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateTenant(ctx, tn.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteTenant(ctx, tn.ID); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"h:status:pending->active", "h:provisioned",
		"h:validate", "h:updated",
		"h:status:active->suspended",
		"h:status:suspended->active",
		"h:deleted",
	}
	if strings.Join(log, ",") != strings.Join(want, ",") {
		t.Errorf("got  %v\nwant %v", log, want)
	}
	if repo.tenants[tn.ID].Status != StatusCancelled {
		t.Error("DeleteTenant should still soft-delete")
	}
}

func TestHooks_UpdateWithStatusChangeFiresStatusChanged(t *testing.T) {
	var log []string
	m, _ := hookedManager(t, &recordingHook{name: "h", log: &log})
	ctx := context.Background()
	tn := &Tenant{Name: "T", Subdomain: "ttt", Status: StatusActive}
	if err := m.CreateTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	log = nil
	tn.Status = StatusSuspended
	if err := m.UpdateTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	want := "h:validate,h:updated,h:status:active->suspended"
	if got := strings.Join(log, ","); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestHooks_RegisterIsConcurrencySafe(t *testing.T) {
	m, _ := hookedManager(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			m.RegisterHook(&BaseHook{})
		}
	}()
	for i := 0; i < 100; i++ {
		_ = m.CreateTenant(context.Background(), &Tenant{Name: "T", Subdomain: uuid.NewString()[:8] + "abc"})
	}
	<-done
}
```

These tests need `MockManagerRepository` to behave like a database: it must store **copies**, not the caller's pointer, otherwise `UpdateTenant`'s `before` snapshot is the same object as the update and status changes are never detected. Replace its `Create`, `GetByID`, `Update` and `Delete` with:

```go
func (m *MockManagerRepository) Create(ctx context.Context, t *Tenant) error {
	if _, exists := m.tenants[t.ID]; exists {
		return errors.New("duplicate id")
	}
	copied := *t
	m.tenants[t.ID] = &copied
	return nil
}

func (m *MockManagerRepository) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return nil, errors.New("tenant not found")
	}
	copied := *t
	return &copied, nil
}

func (m *MockManagerRepository) Update(ctx context.Context, t *Tenant) error {
	if _, ok := m.tenants[t.ID]; !ok {
		return errors.New("tenant not found")
	}
	copied := *t
	m.tenants[t.ID] = &copied
	return nil
}

func (m *MockManagerRepository) Delete(ctx context.Context, id uuid.UUID) error {
	t, ok := m.tenants[id]
	if !ok {
		return errors.New("tenant not found")
	}
	t.Status = StatusCancelled
	return nil
}
```

Keep the mock's existing field name for the map (`tenants`) and its `GetBySubdomain`/`List`/`FindByMetadata` as they are. Re-run the existing `TestManager_*` tests after this change; any that relied on pointer sharing (for example asserting on the caller's struct after `SuspendTenant`) should read the tenant back through `GetTenant` instead.

- [ ] **Step 2: Run and confirm failure**

Run: `go test -count=1 -race -run 'TestHooks' ./tenant/`
Expected: compile errors for `BaseHook`, `HookError`, `RegisterHook`.

- [ ] **Step 3: Implement `tenant/hooks.go`**

```go
package tenant

import (
	"context"
	"errors"
	"fmt"
)

// Hook receives tenant lifecycle events. ValidateMetadata runs before a create
// or update is written and blocks it on error. Every other method runs after
// the write has committed, synchronously, outside any transaction, so it may
// call external services. Embed BaseHook to implement only what you need.
//
// A hook that calls Manager methods will trigger hooks itself; keep that in
// mind to avoid loops.
type Hook interface {
	Name() string
	ValidateMetadata(ctx context.Context, t *Tenant) error
	OnTenantCreated(ctx context.Context, t *Tenant) error
	OnTenantProvisioned(ctx context.Context, t *Tenant) error
	OnTenantUpdated(ctx context.Context, before, after *Tenant) error
	OnTenantStatusChanged(ctx context.Context, t *Tenant, previousStatus string) error
	OnTenantDeleted(ctx context.Context, t *Tenant) error
}

// BaseHook implements every Hook method as a no-op.
type BaseHook struct{}

func (BaseHook) Name() string                                                   { return "hook" }
func (BaseHook) ValidateMetadata(context.Context, *Tenant) error                { return nil }
func (BaseHook) OnTenantCreated(context.Context, *Tenant) error                 { return nil }
func (BaseHook) OnTenantProvisioned(context.Context, *Tenant) error             { return nil }
func (BaseHook) OnTenantUpdated(context.Context, *Tenant, *Tenant) error        { return nil }
func (BaseHook) OnTenantStatusChanged(context.Context, *Tenant, string) error   { return nil }
func (BaseHook) OnTenantDeleted(context.Context, *Tenant) error                 { return nil }

// HookError reports every hook that failed for one event. The tenant write it
// followed has already been committed.
type HookError struct {
	Event  string
	Errors []error
}

func (e *HookError) Error() string {
	return fmt.Sprintf("%d hook(s) failed on %s: %v", len(e.Errors), e.Event, errors.Join(e.Errors...))
}

// Unwrap exposes the individual hook errors to errors.Is / errors.As.
func (e *HookError) Unwrap() []error { return e.Errors }
```

`tenant/interfaces.go`, add to `Manager` after `LimitChecker()`:

```go
	// RegisterHook adds a lifecycle hook. Hooks run in registration order.
	RegisterHook(h Hook)
```

`tenant/manager.go`:
- Add fields `hooks []Hook` and `hooksMu sync.RWMutex` to `manager`; import `"sync"`.
- Add:

```go
// RegisterHook adds a lifecycle hook. Safe to call while requests are running.
func (m *manager) RegisterHook(h Hook) {
	m.hooksMu.Lock()
	defer m.hooksMu.Unlock()
	m.hooks = append(m.hooks, h)
}

// snapshotHooks returns the registered hooks in order.
func (m *manager) snapshotHooks() []Hook {
	m.hooksMu.RLock()
	defer m.hooksMu.RUnlock()
	return append([]Hook(nil), m.hooks...)
}

// validateMetadataHooks runs ValidateMetadata on every hook; the first error blocks the write.
func (m *manager) validateMetadataHooks(ctx context.Context, t *Tenant) error {
	for _, h := range m.snapshotHooks() {
		if err := h.ValidateMetadata(ctx, t); err != nil {
			return &ValidationError{Field: "metadata", Message: fmt.Sprintf("%s: %v", h.Name(), err)}
		}
	}
	return nil
}

// runHooks calls fn for every hook, collects failures, and returns a HookError
// if any failed. Every hook runs even when an earlier one fails.
func (m *manager) runHooks(event string, fn func(Hook) error) error {
	var errs []error
	for _, h := range m.snapshotHooks() {
		if err := fn(h); err != nil {
			m.logger.Error("Lifecycle hook failed",
				zap.String("event", event), zap.String("hook", h.Name()), zap.Error(err))
			errs = append(errs, fmt.Errorf("%s: %w", h.Name(), err))
		}
	}
	if len(errs) > 0 {
		return &HookError{Event: event, Errors: errs}
	}
	return nil
}
```

- `CreateTenant`: after `validateTenant` and before `repository.Create`, call `if err := m.validateMetadataHooks(ctx, tenant); err != nil { return err }`. Ensure `tenant.Metadata` is non-nil (`if tenant.Metadata == nil { tenant.Metadata = TenantMetadata{} }`) before validation. After the successful create and log line, `return m.runHooks("created", func(h Hook) error { return h.OnTenantCreated(ctx, tenant) })`.
- `UpdateTenant`:

```go
func (m *manager) UpdateTenant(ctx context.Context, tenant *Tenant) error {
	if err := m.validateTenant(tenant); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	if tenant.Metadata == nil {
		tenant.Metadata = TenantMetadata{}
	}
	if err := m.validateMetadataHooks(ctx, tenant); err != nil {
		return err
	}
	before, err := m.repository.GetByID(ctx, tenant.ID)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}
	if err := m.repository.Update(ctx, tenant); err != nil {
		return err
	}
	var errs []error
	if err := m.runHooks("updated", func(h Hook) error { return h.OnTenantUpdated(ctx, before, tenant) }); err != nil {
		errs = append(errs, err)
	}
	if before.Status != tenant.Status {
		if err := m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, before.Status) }); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

(import `"errors"`.)
- `ProvisionTenant`: capture `previous := tenant.Status` at the top. Replace the trailing `if tenant.Status == StatusActive { return nil }` block so that after a successful activation it runs:

```go
	var errs []error
	if err := m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, previous) }); err != nil {
		errs = append(errs, err)
	}
	if err := m.runHooks("provisioned", func(h Hook) error { return h.OnTenantProvisioned(ctx, tenant) }); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
```

and returns `nil` (no hooks) when the tenant was already active.
- `SuspendTenant` / `ActivateTenant`: capture `previous := tenant.Status` before changing it; after the successful update, `return m.runHooks("status_changed", func(h Hook) error { return h.OnTenantStatusChanged(ctx, tenant, previous) })`. If `previous` already equals the new status, skip the hooks.
- `DeleteTenant`:

```go
func (m *manager) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := m.repository.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get tenant: %w", err)
	}
	if err := m.repository.Delete(ctx, id); err != nil {
		return err
	}
	tenant.Status = StatusCancelled
	return m.runHooks("deleted", func(h Hook) error { return h.OnTenantDeleted(ctx, tenant) })
}
```

`multitenant.go`: add `Hook = tenant.Hook`, `BaseHook = tenant.BaseHook`, `HookError = tenant.HookError` to the type re-exports.

`multitenant_test.go` `MockMultiTenantManager`: add `func (m *MockMultiTenantManager) RegisterHook(h tenant.Hook) {}`.

- [ ] **Step 4: Integration test that hooks fire against a real database**

Append to `database_integration_test.go`:

```go
type stampingHook struct {
	tenant.BaseHook
	mgr tenant.Manager
}

func (h *stampingHook) Name() string { return "stamper" }
func (h *stampingHook) OnTenantProvisioned(ctx context.Context, t *tenant.Tenant) error {
	t.Metadata.SetString("provisioned_by", "stamper")
	return h.mgr.UpdateTenant(ctx, t)
}

func TestDatabase_Hooks_ProvisionedHookCanPersistMetadata(t *testing.T) {
	tdb := newTestDB(t)
	defer tdb.close()
	mt, _ := migrationTestEnv(t, tdb, 0)
	mt.Manager.RegisterHook(&stampingHook{mgr: mt.Manager})
	ctx := context.Background()

	id := uuid.New()
	t.Cleanup(func() { cleanupTestData(tdb.db, []uuid.UUID{id}) })
	if err := mt.Manager.CreateTenant(ctx, &tenant.Tenant{ID: id, Name: "h", Subdomain: "hook-" + id.String()[:8]}); err != nil {
		t.Fatal(err)
	}
	if err := mt.Manager.ProvisionTenant(ctx, id); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	got, _ := mt.Manager.GetTenant(ctx, id)
	if v, _ := got.Metadata.GetString("provisioned_by"); v != "stamper" {
		t.Errorf("hook-written metadata not persisted: %v", got.Metadata)
	}
	if got.Status != tenant.StatusActive {
		t.Errorf("status = %s", got.Status)
	}
}
```

- [ ] **Step 5: Run everything**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: lifecycle hooks on the tenant Manager

Hook interface with BaseHook for partial implementations. ValidateMetadata
runs before create/update and blocks the write; the other events run after
commit, synchronously, in registration order. Failures are collected into
HookError and returned while the tenant persists.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: Stripe integration example

**Files:**
- Create: `examples/stripe-integration/main.go`, `examples/stripe-integration/stripe_hook.go`, `examples/stripe-integration/stripe_hook_test.go`
- Delete: `examples/stripe-integration/README.md` (if Task 5 did not)

**Interfaces:**
- Consumes: `tenant.Hook`, `tenant.BaseHook`, `tenant.HookError`, `tenant.NewStripeExtension`, `Manager.RegisterHook`, `Manager.UpdateTenant`.

- [ ] **Step 1: Write the failing unit test**

`examples/stripe-integration/stripe_hook_test.go`:

```go
package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

type fakeStripe struct {
	created []string
	deleted []string
	fail    bool
}

func (f *fakeStripe) CreateCustomer(ctx context.Context, name, subdomain string) (string, error) {
	if f.fail {
		return "", errors.New("stripe unavailable")
	}
	id := "cus_" + subdomain
	f.created = append(f.created, id)
	return id, nil
}

func (f *fakeStripe) DeleteCustomer(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// recordingManager stores whatever UpdateTenant is given.
type recordingManager struct {
	tenant.Manager
	updated *tenant.Tenant
}

func (m *recordingManager) UpdateTenant(ctx context.Context, t *tenant.Tenant) error {
	m.updated = t
	return nil
}

func TestStripeHook_CreatesCustomerAndStoresID(t *testing.T) {
	stripe := &fakeStripe{}
	mgr := &recordingManager{}
	h := NewStripeHook(stripe, mgr)
	tn := &tenant.Tenant{ID: uuid.New(), Name: "Acme", Subdomain: "acme", Metadata: tenant.TenantMetadata{}}

	if err := h.OnTenantCreated(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if len(stripe.created) != 1 || stripe.created[0] != "cus_acme" {
		t.Errorf("created = %v", stripe.created)
	}
	if mgr.updated == nil {
		t.Fatal("hook should persist the customer id through UpdateTenant")
	}
	if id, _ := tenant.NewStripeExtension(mgr.updated.Metadata).GetCustomerID(); id != "cus_acme" {
		t.Errorf("stored customer id = %q", id)
	}
}

func TestStripeHook_DeletesCustomerOnTenantDeleted(t *testing.T) {
	stripe := &fakeStripe{}
	h := NewStripeHook(stripe, &recordingManager{})
	tn := &tenant.Tenant{ID: uuid.New(), Metadata: tenant.TenantMetadata{}}
	tenant.NewStripeExtension(tn.Metadata).SetCustomerID("cus_x")

	if err := h.OnTenantDeleted(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if len(stripe.deleted) != 1 || stripe.deleted[0] != "cus_x" {
		t.Errorf("deleted = %v", stripe.deleted)
	}
}

func TestStripeHook_FailureSurfacesAsHookErrorAndTenantPersists(t *testing.T) {
	// Drive the hook through a real manager with mocks so HookError wrapping is exercised.
	repo := newInMemoryRepo()
	mgr := newInMemoryManager(repo)
	stripe := &fakeStripe{fail: true}
	mgr.RegisterHook(NewStripeHook(stripe, mgr))

	err := mgr.CreateTenant(context.Background(), &tenant.Tenant{Name: "Acme", Subdomain: "acme"})
	var herr *tenant.HookError
	if !errors.As(err, &herr) || herr.Event != "created" {
		t.Fatalf("want HookError on created, got %v", err)
	}
	if repo.count() != 1 {
		t.Error("tenant should persist when the Stripe call fails")
	}
}
```

Add the in-memory collaborators below the tests in the same file (imports: add `"errors"`, `"go.uber.org/zap"`):

```go
// memRepo is an in-memory tenant.Repository.
type memRepo struct{ tenants map[uuid.UUID]*tenant.Tenant }

func newInMemoryRepo() *memRepo { return &memRepo{tenants: map[uuid.UUID]*tenant.Tenant{}} }
func (r *memRepo) count() int  { return len(r.tenants) }

func (r *memRepo) Create(ctx context.Context, t *tenant.Tenant) error {
	c := *t
	r.tenants[t.ID] = &c
	return nil
}
func (r *memRepo) GetByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	t, ok := r.tenants[id]
	if !ok {
		return nil, errors.New("not found")
	}
	c := *t
	return &c, nil
}
func (r *memRepo) GetBySubdomain(ctx context.Context, sub string) (*tenant.Tenant, error) {
	for _, t := range r.tenants {
		if t.Subdomain == sub {
			c := *t
			return &c, nil
		}
	}
	return nil, errors.New("not found")
}
func (r *memRepo) Update(ctx context.Context, t *tenant.Tenant) error {
	c := *t
	r.tenants[t.ID] = &c
	return nil
}
func (r *memRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if t, ok := r.tenants[id]; ok {
		t.Status = tenant.StatusCancelled
	}
	return nil
}
func (r *memRepo) List(ctx context.Context, page, perPage int) ([]*tenant.Tenant, int, error) {
	var out []*tenant.Tenant
	for _, t := range r.tenants {
		out = append(out, t)
	}
	return out, len(out), nil
}
func (r *memRepo) FindByMetadata(ctx context.Context, key, value string) ([]*tenant.Tenant, error) {
	return nil, nil
}

// memSchema is an in-memory tenant.SchemaManager.
type memSchema struct{ schemas map[uuid.UUID]bool }

func (s *memSchema) CreateTenantSchema(ctx context.Context, id uuid.UUID) error { s.schemas[id] = true; return nil }
func (s *memSchema) DropTenantSchema(ctx context.Context, id uuid.UUID) error   { delete(s.schemas, id); return nil }
func (s *memSchema) SchemaExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.schemas[id], nil
}
func (s *memSchema) GetSchemaName(id uuid.UUID) string                        { return "tenant_" + id.String() }
func (s *memSchema) ListTenantSchemas(ctx context.Context) ([]string, error) { return nil, nil }

// memMig applies nothing.
type memMig struct{ tenant.MigrationManager }

func (memMig) ApplyPending(ctx context.Context, id uuid.UUID) error { return nil }
func (memMig) GetAppliedMigrations(ctx context.Context, id uuid.UUID) ([]*tenant.Migration, error) {
	return nil, nil
}

// memLimits has no usage tracker.
type memLimits struct{ tenant.LimitChecker }

func (memLimits) GetUsageTracker() tenant.UsageTracker { return nil }
func (memLimits) CheckAllLimits(ctx context.Context, id uuid.UUID) error { return nil }
func (memLimits) GetLimitsForPlan(plan string) tenant.FlexibleLimits    { return tenant.FlexibleLimits{} }

func newInMemoryManager(repo *memRepo) tenant.Manager {
	return tenant.NewManager(tenant.DefaultConfig(), nil, repo, &memSchema{schemas: map[uuid.UUID]bool{}}, memMig{}, memLimits{}, zap.NewNop())
}
```

The embedded nil interfaces in `memMig` and `memLimits` mean any method not listed panics if called; `CreateTenant` only touches the ones shown.

- [ ] **Step 2: Run and confirm compile failure**

Run: `go test -count=1 ./examples/stripe-integration/`
Expected: `undefined: NewStripeHook`.

- [ ] **Step 3: Implement the hook and main**

`examples/stripe-integration/stripe_hook.go`:

```go
package main

import (
	"context"
	"fmt"

	"github.com/alexalmadav/go-multitenant/tenant"
)

// StripeClient is the slice of the Stripe API this example needs.
type StripeClient interface {
	CreateCustomer(ctx context.Context, name, subdomain string) (string, error)
	DeleteCustomer(ctx context.Context, customerID string) error
}

// StripeHook creates a Stripe customer when a tenant is created, stores the
// customer id in tenant metadata, and deletes the customer when the tenant
// is deleted.
type StripeHook struct {
	tenant.BaseHook
	stripe  StripeClient
	manager tenant.Manager
}

func NewStripeHook(stripe StripeClient, manager tenant.Manager) *StripeHook {
	return &StripeHook{stripe: stripe, manager: manager}
}

func (h *StripeHook) Name() string { return "stripe" }

func (h *StripeHook) OnTenantCreated(ctx context.Context, t *tenant.Tenant) error {
	customerID, err := h.stripe.CreateCustomer(ctx, t.Name, t.Subdomain)
	if err != nil {
		return fmt.Errorf("create stripe customer: %w", err)
	}
	if t.Metadata == nil {
		t.Metadata = tenant.TenantMetadata{}
	}
	tenant.NewStripeExtension(t.Metadata).SetCustomerID(customerID)
	// UpdateTenant fires OnTenantUpdated hooks; this hook ignores that event.
	if err := h.manager.UpdateTenant(ctx, t); err != nil {
		return fmt.Errorf("store stripe customer id: %w", err)
	}
	return nil
}

func (h *StripeHook) OnTenantDeleted(ctx context.Context, t *tenant.Tenant) error {
	id, ok := tenant.NewStripeExtension(t.Metadata).GetCustomerID()
	if !ok {
		return nil
	}
	if err := h.stripe.DeleteCustomer(ctx, id); err != nil {
		return fmt.Errorf("delete stripe customer %s: %w", id, err)
	}
	return nil
}
```

`examples/stripe-integration/main.go`:

```go
// Command stripe-integration shows a lifecycle hook that keeps a Stripe
// customer in sync with each tenant. It uses a logging stand-in for the
// Stripe API so it runs without credentials.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/alexalmadav/go-multitenant"
	"github.com/alexalmadav/go-multitenant/tenant"
)

// logStripe prints what a real client would do.
type logStripe struct{}

func (logStripe) CreateCustomer(ctx context.Context, name, subdomain string) (string, error) {
	id := "cus_" + subdomain
	fmt.Printf("stripe: create customer %s for %q\n", id, name)
	return id, nil
}

func (logStripe) DeleteCustomer(ctx context.Context, id string) error {
	fmt.Printf("stripe: delete customer %s\n", id)
	return nil
}

func main() {
	config := multitenant.DefaultConfig()
	config.Database.DSN = os.Getenv("DATABASE_URL")
	config.Database.MigrationsDir = "./migrations" // your tenant schema lives here

	mt, err := multitenant.New(config)
	if err != nil {
		log.Fatal(err)
	}
	defer mt.Close()

	mt.Manager.RegisterHook(NewStripeHook(logStripe{}, mt.Manager))

	ctx := context.Background()
	t := &tenant.Tenant{Name: "Acme Corp", Subdomain: "acme"}
	if err := mt.Manager.CreateTenant(ctx, t); err != nil {
		log.Fatalf("create: %v", err) // a *tenant.HookError here means the tenant exists but Stripe failed
	}
	got, _ := mt.Manager.GetTenant(ctx, t.ID)
	id, _ := tenant.NewStripeExtension(got.Metadata).GetCustomerID()
	fmt.Println("tenant", got.ID, "has stripe customer", id)

	if err := mt.Manager.DeleteTenant(ctx, t.ID); err != nil {
		log.Fatalf("delete: %v", err)
	}
}
```

Delete `examples/stripe-integration/README.md` if it still exists.

- [ ] **Step 4: Run everything**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`, including `examples/stripe-integration`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs(examples): runnable Stripe lifecycle-hook example with tests

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: Documentation

**Files:**
- Modify: `README.md`, `EXTENSIBILITY_GUIDE.md`, `FLEXIBLE_LIMITS.md`, `README_TESTS.md`

No production code. The check is `grep` for stale names plus a read-through.

- [ ] **Step 1: README.md**

- Architecture diagram: replace the example tables under each tenant schema with `└── (tables from your migrations)`.
- Replace the "Tenant Schema Tables" section (near the end) with:

```markdown
### Tenant schema

The library creates no tables in a tenant schema. Point `config.Database.MigrationsDir`
at a directory of `<version>_<name>.up.sql` files (optional matching `.down.sql`).
`ProvisionTenant` creates the schema and applies every file in filename order,
recording each in `public.tenant_migrations`. Add a file later and run
`mt.Migrations.ApplyPendingToAllTenants(ctx)` to bring every active tenant up to
date; new tenants get it automatically.

If `MigrationsDir` is unset, provisioned tenants have an empty schema and `New`
logs a warning. A path that does not exist is an error from `New`.
```

- Limits section: after the `UsageTables` sentence already present, add the config line:

```go
config.Limits.UsageTables = map[string]string{"max_projects": "projects", "max_users": "tenant_users"}
```

and state: "Only limits listed here (or served by a custom `UsageTracker`) are checked."
- Add sections "Metadata" and "Lifecycle hooks" after "Tenant Management":

```markdown
### Metadata

Every tenant has a `Metadata` map stored as JSONB and loaded with the tenant:

```go
t, _ := mt.Manager.GetTenant(ctx, id)
t.Metadata.SetString("custom_domain", "app.acme.com")
tenant.NewStripeExtension(t.Metadata).SetCustomerID("cus_123")
err := mt.Manager.UpdateTenant(ctx, t)
```

### Lifecycle hooks

Register a `tenant.Hook` to react to tenant events. Embed `tenant.BaseHook` and
override what you need. `ValidateMetadata` runs before a write and can block it;
the other events run after the write commits and their errors come back as a
`*tenant.HookError` while the tenant persists.

```go
type auditHook struct{ tenant.BaseHook }
func (auditHook) Name() string { return "audit" }
func (auditHook) OnTenantStatusChanged(ctx context.Context, t *tenant.Tenant, prev string) error {
    log.Printf("tenant %s: %s -> %s", t.ID, prev, t.Status)
    return nil
}

mt.Manager.RegisterHook(auditHook{})
```

See `examples/stripe-integration` for a hook that keeps an external system in sync.
```

- `GetStats` mention under "Managing Tenant Status": `// Returns: SchemaExists, AppliedMigrations, Usage (per limit in UsageTables)`.
- Examples list: replace the extensible-tenant entry with `**[Stripe Integration](./examples/stripe-integration/)**: lifecycle hook keeping a Stripe customer in sync`.

- [ ] **Step 2: EXTENSIBILITY_GUIDE.md**

Rewrite the whole file to three sections mirroring the README additions but longer: "Tenant schema from migrations" (file naming, ordering, `ApplyPending`, resumable provisioning, rollback), "Metadata" (typed getters, `StripeExtension`/`BrandingExtension`, `FindByMetadata` on the repository, indexing note: the GIN index covers `->>` lookups on any key), and "Lifecycle hooks" (event table copied from the spec section 4, failure semantics, re-entrancy note, the Stripe example). Remove every reference to `ExtensibleTenant`, `ExtensibleRepository`, `ExtensionRegistry`, `SchemaRegistry`, `TenantExtension`.

- [ ] **Step 3: FLEXIBLE_LIMITS.md and README_TESTS.md**

`FLEXIBLE_LIMITS.md`: in "Integration with Usage Tracking", add that the default tracker reads `LimitsConfig.UsageTables` and show the map; note that unmapped limits are unchecked.

`README_TESTS.md`: under Integration Tests add "Tenants are provisioned from `testdata/migrations` (projects, tenant_users) via `testConfig`." and add bullets for provisioning, metadata and hooks under section 9.

- [ ] **Step 4: Verify no stale references**

Run: `grep -rn 'ExtensibleTenant\|ExtensibleRepository\|ExtensionRegistry\|SchemaRegistry\|GetTenantDB\|SetSearchPath\|ProjectCount\|UserCount\|StorageUsedGB\|extensible-tenant' --include='*.md' --include='*.go' .`
Expected: no output.

- [ ] **Step 5: Final full run and commit**

Run: `set -o pipefail; gofmt -l .; go build ./... && go vet ./... && go test -short -race ./... && go test -count=1 ./...`
Expected: all `ok`.

```bash
git add -A
git commit -m "docs: tenant schema from migrations, metadata, lifecycle hooks

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

Then push and open the PR with a body listing the breaking changes from the spec's "Breaking changes (v0.7.0)" section.
