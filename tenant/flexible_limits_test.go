package tenant

import (
	"testing"
	"time"
)

func TestLimitValue_Int(t *testing.T) {
	tests := []struct {
		name    string
		limit   LimitValue
		want    int
		wantErr bool
	}{
		{
			name:    "valid int",
			limit:   LimitValue{Type: LimitTypeInt, Value: 10},
			want:    10,
			wantErr: false,
		},
		{
			name:    "valid float64 to int",
			limit:   LimitValue{Type: LimitTypeInt, Value: 10.0},
			want:    10,
			wantErr: false,
		},
		{
			name:    "wrong type",
			limit:   LimitValue{Type: LimitTypeString, Value: 10},
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid value type",
			limit:   LimitValue{Type: LimitTypeInt, Value: "invalid"},
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limit.Int()
			if (err != nil) != tt.wantErr {
				t.Errorf("LimitValue.Int() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("LimitValue.Int() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitValue_Float(t *testing.T) {
	tests := []struct {
		name    string
		limit   LimitValue
		want    float64
		wantErr bool
	}{
		{
			name:    "valid float64",
			limit:   LimitValue{Type: LimitTypeFloat, Value: 10.5},
			want:    10.5,
			wantErr: false,
		},
		{
			name:    "valid int to float64",
			limit:   LimitValue{Type: LimitTypeFloat, Value: 10},
			want:    10.0,
			wantErr: false,
		},
		{
			name:    "wrong type",
			limit:   LimitValue{Type: LimitTypeString, Value: 10.5},
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid value type",
			limit:   LimitValue{Type: LimitTypeFloat, Value: "invalid"},
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limit.Float()
			if (err != nil) != tt.wantErr {
				t.Errorf("LimitValue.Float() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("LimitValue.Float() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitValue_String(t *testing.T) {
	tests := []struct {
		name    string
		limit   LimitValue
		want    string
		wantErr bool
	}{
		{
			name:    "valid string",
			limit:   LimitValue{Type: LimitTypeString, Value: "test"},
			want:    "test",
			wantErr: false,
		},
		{
			name:    "wrong type",
			limit:   LimitValue{Type: LimitTypeInt, Value: "test"},
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid value type",
			limit:   LimitValue{Type: LimitTypeString, Value: 123},
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limit.String()
			if (err != nil) != tt.wantErr {
				t.Errorf("LimitValue.String() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("LimitValue.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitValue_Bool(t *testing.T) {
	tests := []struct {
		name    string
		limit   LimitValue
		want    bool
		wantErr bool
	}{
		{
			name:    "valid bool true",
			limit:   LimitValue{Type: LimitTypeBool, Value: true},
			want:    true,
			wantErr: false,
		},
		{
			name:    "valid bool false",
			limit:   LimitValue{Type: LimitTypeBool, Value: false},
			want:    false,
			wantErr: false,
		},
		{
			name:    "wrong type",
			limit:   LimitValue{Type: LimitTypeInt, Value: true},
			want:    false,
			wantErr: true,
		},
		{
			name:    "invalid value type",
			limit:   LimitValue{Type: LimitTypeBool, Value: "true"},
			want:    false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limit.Bool()
			if (err != nil) != tt.wantErr {
				t.Errorf("LimitValue.Bool() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("LimitValue.Bool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitValue_Duration(t *testing.T) {
	tests := []struct {
		name    string
		limit   LimitValue
		want    time.Duration
		wantErr bool
	}{
		{
			name:    "valid duration string",
			limit:   LimitValue{Type: LimitTypeDuration, Value: "5m"},
			want:    5 * time.Minute,
			wantErr: false,
		},
		{
			name:    "valid int64 duration",
			limit:   LimitValue{Type: LimitTypeDuration, Value: int64(300000000000)},
			want:    5 * time.Minute,
			wantErr: false,
		},
		{
			name:    "valid float64 duration",
			limit:   LimitValue{Type: LimitTypeDuration, Value: float64(300000000000)},
			want:    5 * time.Minute,
			wantErr: false,
		},
		{
			name:    "wrong type",
			limit:   LimitValue{Type: LimitTypeInt, Value: "5m"},
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid duration string",
			limit:   LimitValue{Type: LimitTypeDuration, Value: "invalid"},
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.limit.Duration()
			if (err != nil) != tt.wantErr {
				t.Errorf("LimitValue.Duration() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("LimitValue.Duration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitValue_IsUnlimited(t *testing.T) {
	tests := []struct {
		name  string
		limit LimitValue
		want  bool
	}{
		{
			name:  "unlimited int -1",
			limit: LimitValue{Type: LimitTypeInt, Value: -1},
			want:  true,
		},
		{
			name:  "limited int",
			limit: LimitValue{Type: LimitTypeInt, Value: 10},
			want:  false,
		},
		{
			name:  "unlimited float -1",
			limit: LimitValue{Type: LimitTypeFloat, Value: -1.0},
			want:  true,
		},
		{
			name:  "limited float",
			limit: LimitValue{Type: LimitTypeFloat, Value: 10.5},
			want:  false,
		},
		{
			name:  "unlimited string",
			limit: LimitValue{Type: LimitTypeString, Value: "unlimited"},
			want:  true,
		},
		{
			name:  "limited string",
			limit: LimitValue{Type: LimitTypeString, Value: "limited"},
			want:  false,
		},
		{
			name:  "bool is never unlimited",
			limit: LimitValue{Type: LimitTypeBool, Value: true},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.limit.IsUnlimited(); got != tt.want {
				t.Errorf("LimitValue.IsUnlimited() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFlexibleLimits_Set(t *testing.T) {
	limits := make(FlexibleLimits)

	// Test setting int
	limits.Set("max_users", LimitTypeInt, 10)
	if val, exists := limits.Get("max_users"); !exists {
		t.Error("Failed to set int limit")
	} else if intVal, err := val.Int(); err != nil || intVal != 10 {
		t.Errorf("Set int limit failed: got %v, want 10", intVal)
	}

	// Test setting float
	limits.Set("max_storage", LimitTypeFloat, 5.5)
	if val, exists := limits.Get("max_storage"); !exists {
		t.Error("Failed to set float limit")
	} else if floatVal, err := val.Float(); err != nil || floatVal != 5.5 {
		t.Errorf("Set float limit failed: got %v, want 5.5", floatVal)
	}

	// Test setting string
	limits.Set("region", LimitTypeString, "us-east")
	if val, exists := limits.Get("region"); !exists {
		t.Error("Failed to set string limit")
	} else if strVal, err := val.String(); err != nil || strVal != "us-east" {
		t.Errorf("Set string limit failed: got %v, want us-east", strVal)
	}

	// Test setting bool
	limits.Set("advanced_features", LimitTypeBool, true)
	if val, exists := limits.Get("advanced_features"); !exists {
		t.Error("Failed to set bool limit")
	} else if boolVal, err := val.Bool(); err != nil || !boolVal {
		t.Errorf("Set bool limit failed: got %v, want true", boolVal)
	}
}

func TestFlexibleLimits_Get(t *testing.T) {
	limits := make(FlexibleLimits)
	limits["max_users"] = &LimitValue{Type: LimitTypeInt, Value: 10}

	// Test existing limit
	if val, exists := limits.Get("max_users"); !exists {
		t.Error("Failed to get existing limit")
	} else if intVal, err := val.Int(); err != nil || intVal != 10 {
		t.Errorf("Get limit failed: got %v, want 10", intVal)
	}

	// Test non-existing limit
	if _, exists := limits.Get("non_existing"); exists {
		t.Error("Get should return false for non-existing limit")
	}
}

func TestFlexibleLimits_GetInt(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("max_users", LimitTypeInt, 10)
	limits.Set("max_storage", LimitTypeFloat, 5.5)

	// Test valid int
	if val, err := limits.GetInt("max_users"); err != nil || val != 10 {
		t.Errorf("GetInt failed: got %v, want 10, error: %v", val, err)
	}

	// Test non-existing
	if _, err := limits.GetInt("non_existing"); err == nil {
		t.Error("GetInt should return error for non-existing limit")
	}

	// Test wrong type
	if _, err := limits.GetInt("max_storage"); err == nil {
		t.Error("GetInt should return error for wrong type")
	}
}

func TestFlexibleLimits_GetFloat(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("max_storage", LimitTypeFloat, 5.5)
	limits.Set("max_users", LimitTypeInt, 10)

	// Test valid float
	if val, err := limits.GetFloat("max_storage"); err != nil || val != 5.5 {
		t.Errorf("GetFloat failed: got %v, want 5.5, error: %v", val, err)
	}

	// Test non-existing
	if _, err := limits.GetFloat("non_existing"); err == nil {
		t.Error("GetFloat should return error for non-existing limit")
	}

	// Test wrong type
	if _, err := limits.GetFloat("max_users"); err == nil {
		t.Error("GetFloat should return error for wrong type")
	}
}

func TestFlexibleLimits_GetString(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("region", LimitTypeString, "us-east")
	limits.Set("max_users", LimitTypeInt, 10)

	// Test valid string
	if val, err := limits.GetString("region"); err != nil || val != "us-east" {
		t.Errorf("GetString failed: got %v, want us-east, error: %v", val, err)
	}

	// Test non-existing
	if _, err := limits.GetString("non_existing"); err == nil {
		t.Error("GetString should return error for non-existing limit")
	}

	// Test wrong type
	if _, err := limits.GetString("max_users"); err == nil {
		t.Error("GetString should return error for wrong type")
	}
}

func TestFlexibleLimits_GetBool(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("advanced_features", LimitTypeBool, true)
	limits.Set("max_users", LimitTypeInt, 10)

	// Test valid bool
	if val, err := limits.GetBool("advanced_features"); err != nil || !val {
		t.Errorf("GetBool failed: got %v, want true, error: %v", val, err)
	}

	// Test non-existing
	if _, err := limits.GetBool("non_existing"); err == nil {
		t.Error("GetBool should return error for non-existing limit")
	}

	// Test wrong type
	if _, err := limits.GetBool("max_users"); err == nil {
		t.Error("GetBool should return error for wrong type")
	}
}

func TestFlexibleLimits_GetDuration(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("timeout", LimitTypeDuration, "5m")
	limits.Set("max_users", LimitTypeInt, 10)

	// Test valid duration
	if val, err := limits.GetDuration("timeout"); err != nil || val != 5*time.Minute {
		t.Errorf("GetDuration failed: got %v, want 5m, error: %v", val, err)
	}

	// Test non-existing
	if _, err := limits.GetDuration("non_existing"); err == nil {
		t.Error("GetDuration should return error for non-existing limit")
	}

	// Test wrong type
	if _, err := limits.GetDuration("max_users"); err == nil {
		t.Error("GetDuration should return error for wrong type")
	}
}

func TestFlexibleLimits_Delete(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("max_users", LimitTypeInt, 10)
	limits.Set("max_storage", LimitTypeFloat, 5.5)

	// Verify limit exists
	if _, exists := limits.Get("max_users"); !exists {
		t.Error("Limit should exist before deletion")
	}

	// Delete limit
	limits.Delete("max_users")

	// Verify limit is deleted
	if _, exists := limits.Get("max_users"); exists {
		t.Error("Limit should not exist after deletion")
	}

	// Verify other limits still exist
	if _, exists := limits.Get("max_storage"); !exists {
		t.Error("Other limits should still exist after deletion")
	}
}

func TestFlexibleLimits_Has(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("max_users", LimitTypeInt, 10)

	// Test existing limit
	if !limits.Has("max_users") {
		t.Error("Has should return true for existing limit")
	}

	// Test non-existing limit
	if limits.Has("non_existing") {
		t.Error("Has should return false for non-existing limit")
	}
}

func TestFlexibleLimits_Keys(t *testing.T) {
	limits := make(FlexibleLimits)
	limits.Set("max_users", LimitTypeInt, 10)
	limits.Set("max_storage", LimitTypeFloat, 5.5)
	limits.Set("region", LimitTypeString, "us-east")

	keys := limits.Keys()

	if len(keys) != 3 {
		t.Errorf("Keys() returned %d keys, want 3", len(keys))
	}

	expectedKeys := map[string]bool{
		"max_users":   true,
		"max_storage": true,
		"region":      true,
	}

	for _, key := range keys {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in Keys(): %s", key)
		}
		delete(expectedKeys, key)
	}

	if len(expectedKeys) > 0 {
		t.Errorf("Missing keys in Keys(): %v", expectedKeys)
	}
}

func TestFlexibleLimits_Len(t *testing.T) {
	limits := make(FlexibleLimits)

	// Test empty limits
	if limits.Len() != 0 {
		t.Errorf("Len() = %d, want 0", limits.Len())
	}

	// Add limits and test
	limits.Set("max_users", LimitTypeInt, 10)
	if limits.Len() != 1 {
		t.Errorf("Len() = %d, want 1", limits.Len())
	}

	limits.Set("max_storage", LimitTypeFloat, 5.5)
	if limits.Len() != 2 {
		t.Errorf("Len() = %d, want 2", limits.Len())
	}

	// Delete and test
	limits.Delete("max_users")
	if limits.Len() != 1 {
		t.Errorf("Len() = %d, want 1 after deletion", limits.Len())
	}
}

func TestLimitDefinition(t *testing.T) {
	def := LimitDefinition{
		Name:        "max_users",
		DisplayName: "Maximum Users",
		Description: "Maximum number of users allowed",
		Type:        LimitTypeInt,
		Required:    true,
		DefaultValue: &LimitValue{
			Type:  LimitTypeInt,
			Value: 5,
		},
		MinValue: &LimitValue{
			Type:  LimitTypeInt,
			Value: 1,
		},
		MaxValue: &LimitValue{
			Type:  LimitTypeInt,
			Value: 1000,
		},
		Category: "users",
		Tags:     []string{"core", "billing"},
	}

	// Test fields
	if def.Name != "max_users" {
		t.Errorf("LimitDefinition.Name = %v, want max_users", def.Name)
	}
	if def.Type != LimitTypeInt {
		t.Errorf("LimitDefinition.Type = %v, want %v", def.Type, LimitTypeInt)
	}
	if !def.Required {
		t.Error("LimitDefinition.Required should be true")
	}
	if def.Category != "users" {
		t.Errorf("LimitDefinition.Category = %v, want users", def.Category)
	}
	if len(def.Tags) != 2 {
		t.Errorf("LimitDefinition.Tags length = %d, want 2", len(def.Tags))
	}
}

func TestDefaultLimitSchema(t *testing.T) {
	schema := DefaultLimitSchema()

	if schema == nil {
		t.Error("DefaultLimitSchema() should not return nil")
	}

	// Test that it has some default definitions
	definitions := schema.GetAllDefinitions()
	if len(definitions) == 0 {
		t.Error("DefaultLimitSchema() should have some default limit definitions")
	}

	// Test specific common limits
	if _, exists := schema.GetDefinition("max_users"); !exists {
		t.Error("DefaultLimitSchema() should have max_users definition")
	}
	if _, exists := schema.GetDefinition("max_projects"); !exists {
		t.Error("DefaultLimitSchema() should have max_projects definition")
	}
}
