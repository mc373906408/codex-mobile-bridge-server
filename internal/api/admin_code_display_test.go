package api

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func TestAdministratorCodeDisplaySeparatesCodesFromRecordIDs(t *testing.T) {
	for language, text := range pageLocales {
		for _, hosted := range []bool{false, true} {
			var output bytes.Buffer
			if err := adminTemplate.Execute(&output, map[string]any{
				"Language": language, "Text": text, "Hosted": hosted,
				"Account": store.Account{ID: "template-admin", Login: "operator"},
			}); err != nil {
				t.Fatal(err)
			}
			page := output.String()
			if strings.Contains(page, `id="generated-code-list"`) != hosted {
				t.Fatal("only hosted administrators may display generated redemption codes", language)
			}
			if hosted && (!strings.Contains(page, text["generated_codes_heading"]) ||
				!strings.Contains(page, `<th scope="col">`+text["code_column"]+`</th>`) ||
				strings.Contains(page, `<th scope="col">`+text["code_id_column"]+`</th>`)) {
				t.Fatal("the primary code column must not label record IDs as redemption codes", language)
			}
		}
	}
	data, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	for _, fragment := range []string{
		`navigator.clipboard.writeText(code)`, `generated.code, "code-value"`,
		`"summary", t("code_id_column")`, `t("code_plaintext_unavailable")`,
		`generatedCodes.clear()`, `window.addEventListener("pagehide", clearCodeExport)`,
	} {
		if !strings.Contains(js, fragment) {
			t.Fatal("generated-code display or plaintext cleanup contract is missing", fragment)
		}
	}
	for _, fragment := range []string{"localStorage", "sessionStorage", "innerHTML", "writeText(record.id)"} {
		if strings.Contains(js, fragment) {
			t.Fatal("the code display must not persist plaintext or copy a record ID", fragment)
		}
	}
}
