package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestLocalesCoverTemplateAndHaveMatchingPlaceholders(t *testing.T) {
	if len(pageLocales) != 2 || len(pageLocales["en"]) == 0 {
		t.Fatal("both embedded locales are required")
	}
	placeholders := regexp.MustCompile(`\{\w+\}`)
	for key, english := range pageLocales["en"] {
		chinese, ok := pageLocales["zh-CN"][key]
		if !ok || english == "" || chinese == "" {
			t.Fatal("missing or empty translation", key)
		}
		en := placeholders.FindAllString(english, -1)
		zh := placeholders.FindAllString(chinese, -1)
		sort.Strings(en)
		sort.Strings(zh)
		if !reflect.DeepEqual(en, zh) {
			t.Fatal("translation placeholders differ", key)
		}
	}
	if len(pageLocales["en"]) != len(pageLocales["zh-CN"]) {
		t.Fatal("locale key sets differ")
	}
	for _, name := range []string{"web/index.html", "web/admin.html"} {
		data, err := webFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range regexp.MustCompile(`\.Text\.(\w+)`).FindAllSubmatch(data, -1) {
			if _, ok := pageLocales["en"][string(match[1])]; !ok {
				t.Fatal("template references an untranslated key", name, string(match[1]))
			}
		}
	}
	for language, text := range pageLocales {
		var output bytes.Buffer
		if err := pageTemplate.Execute(&output, map[string]any{"Language": language, "Text": text, "Hosted": true, "Login": true}); err != nil {
			t.Fatal("translated guest template failed", err)
		}
		for _, fragment := range []string{`lang="` + language + `"`, text["admin_heading"], `name="login" type="text"`, `method="post"`} {
			if !strings.Contains(output.String(), fragment) {
				t.Fatal("translated administrator guest template is incomplete", language)
			}
		}
		if strings.Contains(output.String(), "<no value>") || strings.Contains(output.String(), `id="register"`) {
			t.Fatal("the website must be fully translated and administrator-only", language)
		}
		if strings.Contains(output.String(), `name="setup_code"`) || strings.Contains(output.String(), `id="setup-code"`) {
			t.Fatal("administrator login must not ask for a manual initialization code", language)
		}
		output.Reset()
		if err := adminTemplate.Execute(&output, map[string]any{"Language": language, "Text": text, "Hosted": true,
			"Account": store.Account{ID: "template-admin", Login: "operator"}}); err != nil {
			t.Fatal("translated management template failed", err)
		}
		for _, fragment := range []string{`data-admin="true"`, `class="admin-nav"`, `id="users"`, `id="audit"`, `id="codes"`, `id="code-list"`, `id="generated-code-list"`, text["overview_heading"], text["codes_heading"], `<th scope="col">` + text["code_column"] + `</th>`} {
			if !strings.Contains(output.String(), fragment) {
				t.Fatal("translated management template is incomplete", fragment, language)
			}
		}
		for _, fragment := range []string{`class="intro"`, `id="redeem"`, `id="entitlement"`, "<no value>"} {
			if strings.Contains(output.String(), fragment) {
				t.Fatal("management must be separate from the homepage and client redemption", fragment)
			}
		}
	}
}

func TestPageLanguageDefaultsToChineseAndPersistsExplicitChoice(t *testing.T) {
	r := httptest.NewRequest("GET", "https://relay.example.test/", nil)
	w := httptest.NewRecorder()
	if language, _ := pageLanguage(w, r, false); language != "zh-CN" {
		t.Fatal("default page must be Chinese")
	}
	r = httptest.NewRequest("GET", "https://relay.example.test/admin?lang=en", nil)
	w = httptest.NewRecorder()
	if language, _ := pageLanguage(w, r, false); language != "en" {
		t.Fatal("explicit English selection must work")
	}
	cookie := responseCookie(t, w, "bridge_language")
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("language preference must use a safe, same-origin cookie")
	}
	r = httptest.NewRequest("GET", "https://relay.example.test/", nil)
	r.AddCookie(cookie)
	if language, _ := pageLanguage(httptest.NewRecorder(), r, false); language != "en" {
		t.Fatal("language preference must survive navigation")
	}
	r = httptest.NewRequest("GET", "https://relay.example.test/?lang=../../other", nil)
	r.AddCookie(&http.Cookie{Name: "bridge_language", Value: "unsupported"})
	if language, _ := pageLanguage(httptest.NewRecorder(), r, false); language != "zh-CN" {
		t.Fatal("unsupported language values must not select arbitrary assets")
	}
}

func TestLocaleAssetsAndCapabilitiesDeclareAdministratorOnlyWeb(t *testing.T) {
	a, err := New(testutil.Config(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	for _, language := range []string{"en", "zh-CN"} {
		w := webRequest(t, a, "GET", "/assets/locale_"+language+".json", nil, nil)
		expectStatus(t, w, 200, "")
		var text map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &text); err != nil || !reflect.DeepEqual(text, pageLocales[language]) {
			t.Fatal("browser and server must use the same embedded translations", err)
		}
		if w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("locale asset headers are incomplete")
		}
	}
	expectStatus(t, webRequest(t, a, "GET", "/assets/locale_unknown.json", nil, nil), 404, "")
	w := webRequest(t, a, "GET", "/v1/capabilities", nil, nil)
	var capabilities map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &capabilities); err != nil || capabilities["web_access"] != "administrator_only" || capabilities["administrator_login"] != "username" {
		t.Fatal("capabilities must distinguish client and administrator website access", err)
	}
	if capabilities["redemption_web"] != false || capabilities["web_console"] != "/admin" || capabilities["web_home"] != "/" {
		t.Fatal("capabilities must separate client redemption, homepage and management")
	}
}
