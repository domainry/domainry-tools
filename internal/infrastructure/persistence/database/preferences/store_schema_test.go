package preferences

import (
	"strings"
	"testing"

	"github.com/domainry/domainry-foundation/schemaownership"
	"github.com/domainry/domainry-orm/dialect"
)

func TestPreferenceSchemaUsesPhysicalIDWithoutEntityID(t *testing.T) {
	tables := SchemaOwnership()
	if err := schemaownership.ValidateAll(tables); err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Kind != schemaownership.KindState || tables[0].EntityIDColumn != "" || len(tables[0].BusinessKey) != 0 {
		t.Fatalf("preferences manufactured an entity identity: %#v", tables)
	}
	renderer, err := dialect.ParseRenderer("mysql", "", "")
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := Migrations(renderer)
	if err != nil || len(migrations) != 1 || len(migrations[0].Statements) != 1 {
		t.Fatalf("preference migrations=%#v err=%v", migrations, err)
	}
	statement := strings.ToUpper(migrations[0].Statements[0])
	if !strings.Contains(statement, "`ID` BIGINT NOT NULL AUTO_INCREMENT") || !strings.Contains(statement, "PRIMARY KEY (`ID`)") || !strings.Contains(statement, "UNIQUE (`OWNER_KEY`, `TOOL_KEY`)") {
		t.Fatalf("preference physical schema does not match ownership: %s", statement)
	}
}
