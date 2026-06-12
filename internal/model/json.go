package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// JSONValue stores raw JSON using the database JSON type.
// PostgreSQL is intentionally pinned to JSON rather than JSONB so Atlas/GORM
// can generate ordinary JSON migrations across supported dialects.
type JSONValue []byte

func NewJSONValue(value interface{}) JSONValue {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return JSONValue(raw)
}

func (j JSONValue) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	if !json.Valid(j) {
		return nil, errors.New("invalid json value")
	}
	return j, nil
}

func (j *JSONValue) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*j = nil
		return nil
	}
	if !json.Valid(data) {
		return errors.New("invalid json value")
	}
	*j = append((*j)[:0], data...)
	return nil
}

func (j JSONValue) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	if !json.Valid(j) {
		return nil, errors.New("invalid json value")
	}
	return string(j), nil
}

func (j *JSONValue) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = append((*j)[:0], v...)
	default:
		return fmt.Errorf("unsupported json scan type %T", value)
	}
	if len(*j) > 0 && !json.Valid(*j) {
		return errors.New("invalid json value")
	}
	return nil
}

func (JSONValue) GormDataType() string {
	return "json"
}

func (JSONValue) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	return "JSON"
}
