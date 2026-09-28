package logger

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap/zapcore"
)

func TestDatabaseErrorFieldsKeepCodesWithoutPrivateData(t *testing.T) {
	driverErr := &mysql.MySQLError{Number: 1064, SQLState: [5]byte{'4', '2', '0', '0', '0'}, Message: "private SQL and credentials"}
	for _, err := range []error{driverErr, fmt.Errorf("private wrapper: %w", driverErr)} {
		encoded := zapcore.NewMapObjectEncoder()
		for _, field := range DatabaseErrorFields(err) {
			field.AddTo(encoded)
		}
		if len(encoded.Fields) != 3 || encoded.Fields["mysql_errno"] != uint16(1064) || encoded.Fields["sql_state"] != "42000" || encoded.Fields["error_type"] != fmt.Sprintf("%T", err) {
			t.Fatalf("unexpected database diagnostics: %v", encoded.Fields)
		}
	}
	encoded := zapcore.NewMapObjectEncoder()
	for _, field := range DatabaseErrorFields(errors.New("private message")) {
		field.AddTo(encoded)
	}
	if len(encoded.Fields) != 1 || encoded.Fields["error_type"] != "*errors.errorString" {
		t.Fatalf("unexpected non-MySQL diagnostics: %v", encoded.Fields)
	}
}
