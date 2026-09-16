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
func (fakeSchemaManager) GetSchemaName(id uuid.UUID) string                       { return "tenant_x" }
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
