package qdrantutil

import "github.com/qdrant/go-client/qdrant"

// GetPayloadString extracts a string value from a Qdrant payload map.
func GetPayloadString(payload map[string]*qdrant.Value, key string) (string, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return "", false
	}
	return v.GetStringValue(), true
}

// GetPayloadStringList extracts a list of strings from a Qdrant payload.
func GetPayloadStringList(payload map[string]*qdrant.Value, key string) []string {
	v, ok := payload[key]
	if !ok || v == nil {
		return nil
	}
	if s := v.GetStringValue(); s != "" {
		return []string{s}
	}
	values := v.GetListValue()
	if values == nil {
		return nil
	}
	items := values.GetValues()
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s := item.GetStringValue(); s != "" {
			result = append(result, s)
		}
	}
	return result
}

// GetPayloadFloat extracts a float64 from a Qdrant payload.
func GetPayloadFloat(payload map[string]*qdrant.Value, key string) (float64, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return 0, false
	}
	return v.GetDoubleValue(), true
}

// GetPayloadBool extracts a bool from a Qdrant payload.
func GetPayloadBool(payload map[string]*qdrant.Value, key string) (bool, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return false, false
	}
	return v.GetBoolValue(), true
}

// GetPayloadInt extracts an integer from a Qdrant payload (stored as double).
func GetPayloadInt(payload map[string]*qdrant.Value, key string) (int, bool) {
	f, ok := GetPayloadFloat(payload, key)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// GetPayloadStringValue is a convenience wrapper returning zero-value semantics.
func GetPayloadStringValue(payload map[string]*qdrant.Value, key string) string {
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	return v.GetStringValue()
}

// GetPayloadFloatValue is a convenience wrapper returning zero-value semantics.
func GetPayloadFloatValue(payload map[string]*qdrant.Value, key string) float64 {
	v, ok := payload[key]
	if !ok || v == nil {
		return 0
	}
	return v.GetDoubleValue()
}

// GetPayloadBoolValue is a convenience wrapper returning zero-value semantics.
func GetPayloadBoolValue(payload map[string]*qdrant.Value, key string) bool {
	v, ok := payload[key]
	if !ok || v == nil {
		return false
	}
	return v.GetBoolValue()
}

// GetPayloadIntValue is a convenience wrapper returning zero-value semantics.
func GetPayloadIntValue(payload map[string]*qdrant.Value, key string) int {
	return int(GetPayloadFloatValue(payload, key))
}
