package extract

import (
	"os"
	"path/filepath"
	"testing"
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
