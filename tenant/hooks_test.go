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
func (h *recordingHook) ValidateMetadata(ctx context.Context, t *Tenant) error {
	return h.record("validate")
}
func (h *recordingHook) OnTenantCreated(ctx context.Context, t *Tenant) error {
	return h.record("created")
}
func (h *recordingHook) OnTenantProvisioned(ctx context.Context, t *Tenant) error {
	return h.record("provisioned")
}
func (h *recordingHook) OnTenantUpdated(ctx context.Context, before, after *Tenant) error {
	return h.record("updated")
}
func (h *recordingHook) OnTenantStatusChanged(ctx context.Context, t *Tenant, prev string) error {
	return h.record("status:" + prev + "->" + t.Status)
}
func (h *recordingHook) OnTenantDeleted(ctx context.Context, t *Tenant) error {
	return h.record("deleted")
}

func hookedManager(t *testing.T, hooks ...Hook) (Manager, *MockManagerRepository) {
	t.Helper()
	repo, schema, mig := newManagerMocks()
	m := NewManager(DefaultConfig(), nil, repo, schema, mig, zap.NewNop())
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
	// Re-fetch before mutating: ProvisionTenant advanced the repository's
	// status via its own GetByID copy, which never touched tn (the mock
	// repository stores/returns copies, so tn is stale after Provision).
	// Passing the stale tn to UpdateTenant would revert the persisted
	// status and fire a spurious status_changed event.
	tn, err := m.GetTenant(ctx, tn.ID)
	if err != nil {
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
