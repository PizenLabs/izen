package templates

import (
	"bytes"
	"embed"
	"strings"
	"text/template"
)

//go:embed licenses/*.tpl
var embedFS embed.FS

var registry map[string]*template.Template

func init() {
	registry = make(map[string]*template.Template)
	licenses, _ := embedFS.ReadDir("licenses")
	for _, entry := range licenses {
		name := entry.Name()
		data, _ := embedFS.ReadFile("licenses/" + name)
		tmpl, err := template.New(name).Parse(string(data))
		if err != nil {
			continue
		}
		key := licenseKey(name)
		registry[key] = tmpl
	}
}

func licenseKey(filename string) string {
	s := strings.TrimSuffix(filename, ".tpl")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ToLower(s)
	return s
}

func RenderLicense(licenseType, prompt string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(licenseType))
	tmpl, ok := registry[key]
	if !ok {
		return "", false
	}
	params := ExtractLicenseParams(prompt)
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", false
	}
	return buf.String(), true
}
