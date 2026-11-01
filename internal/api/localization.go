package api

import (
	"encoding/json"
	"net/http"
)

var pageLocales = func() map[string]map[string]string {
	locales := make(map[string]map[string]string)
	for _, language := range []string{"zh-CN", "en"} {
		data, err := webFiles.ReadFile("web/locale_" + language + ".json")
		if err != nil {
			panic(err)
		}
		var text map[string]string
		if err := json.Unmarshal(data, &text); err != nil {
			panic(err)
		}
		locales[language] = text
	}
	return locales
}()

func pageLanguage(w http.ResponseWriter, r *http.Request, development bool) (string, map[string]string) {
	language := r.URL.Query().Get("lang")
	if _, ok := pageLocales[language]; ok {
		http.SetCookie(w, &http.Cookie{Name: "bridge_language", Value: language, Path: "/", MaxAge: 365 * 24 * 3600,
			Secure: !development, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	} else {
		language = "zh-CN"
		if cookie, err := r.Cookie("bridge_language"); err == nil {
			if _, ok := pageLocales[cookie.Value]; ok {
				language = cookie.Value
			}
		}
	}
	return language, pageLocales[language]
}
