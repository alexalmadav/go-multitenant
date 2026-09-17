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

func TestTenantMetadata_ScanJSONNullYieldsEmptyMap(t *testing.T) {
	var out TenantMetadata
	if err := out.Scan([]byte("null")); err != nil {
		t.Fatalf("Scan([]byte(\"null\")): %v", err)
	}
	if out == nil {
		t.Fatal("Scan of JSON null should leave a non-nil empty map")
	}
}

func TestTenantMetadata_NilValueIsEmptyObject(t *testing.T) {
	var m TenantMetadata
	v, err := m.Value()
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := v.(string); !ok || s != "{}" {
		t.Errorf("nil metadata should serialize as the string \"{}\" (simple protocol requires a string, not []byte), got %#v", v)
	}
}

func TestTenant_HasMetadataField(t *testing.T) {
	tn := Tenant{Metadata: TenantMetadata{"k": "v"}}
	if s, _ := tn.Metadata.GetString("k"); s != "v" {
		t.Errorf("Tenant.Metadata not wired")
	}
}
