package utils

import "encoding/json"

func MarshalIndentJsonToString(v interface{}, indent string) string {
	switch v.(type) {
	case string:
		return v.(string)
	case *string:
		return *v.(*string)
	}

	data, _ := json.MarshalIndent(v, "", indent)
	return string(data)
}

func MarshalJsonToString(v interface{}) string {
	switch v.(type) {
	case string:
		return v.(string)
	case *string:
		return *v.(*string)
	}

	data, _ := json.Marshal(v)
	return string(data)
}
