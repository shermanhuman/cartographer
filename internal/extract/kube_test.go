package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/facts"
)

func TestKubernetesKeepsOnlyURLEnv(t *testing.T) {
	root := t.TempDir()
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: default}
spec:
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - name: web
          image: ghcr.io/x/web:latest
          env:
            - {name: ADMIN_URL, value: "http://admin.default.svc/api/v1"}
            - {name: SITE_KEY, value: "0x4AAA-not-a-url"}
            - {name: PORT, value: "4000"}
---
apiVersion: v1
kind: Service
metadata: {name: web}
spec: {selector: {app: web}}
`
	if err := os.WriteFile(filepath.Join(root, "web.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Kubernetes("infra", root, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Services) != 2 {
		t.Fatalf("services: %+v", d.Services)
	}
	env := d.Services[0].Env
	if len(env) != 1 || env["ADMIN_URL"] != "http://admin.default.svc/api/v1" {
		t.Errorf("env kept %v, want only ADMIN_URL", env)
	}
	if d.Services[0].Image != "ghcr.io/x/web:latest" || d.Services[1].Selector["app"] != "web" {
		t.Errorf("image or selector: %+v", d.Services)
	}
}

// A malformed YAML document is reported as a parse_error note in both
// documents, and the file's later documents are still read.
func TestMalformedYAMLDocumentIsReportedAndSkipped(t *testing.T) {
	root := t.TempDir()
	manifest := `apiVersion: v1
kind: Service
metadata: {name: first}
spec: {selector: {app: first}}
---
apiVersion: v1
kind: Service
metadata: {name: [broken
---
apiVersion: v1
kind: Service
metadata: {name: second}
spec: {selector: {app: second}}
---
cold:
  admin_url: http://admin.shop.svc/api
`
	if err := os.WriteFile(filepath.Join(root, "k8s.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Kubernetes("infra", root, "abc")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range d.Services {
		names = append(names, s.Name)
	}
	if len(names) != 2 || names[0] != "first" || names[1] != "second" {
		t.Errorf("services %v, want first and second", names)
	}
	u, err := ConfigURLs("infra", root, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.ConfigURLs) != 1 || u.ConfigURLs[0].Line != 16 {
		t.Errorf("config urls %+v, want cold.admin_url at line 16", u.ConfigURLs)
	}
	for _, doc := range []*facts.Doc{d, u} {
		if len(doc.Notes) != 1 || doc.Notes[0].Kind != "parse_error" || doc.Notes[0].File != "k8s.yaml" || !strings.Contains(doc.Notes[0].Message, "document at line 6: ") {
			t.Errorf("%s notes %+v, want one parse_error for k8s.yaml's document at line 6", doc.Producer.Name, doc.Notes)
		}
	}
}
