// Package extract holds cartographer's built-in extractors that need no
// language toolchain: Kubernetes manifests and URLs in configuration files.
// Each returns one facts version 2 document.
package extract

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/facts"
	"go.yaml.in/yaml/v3"
)

// Producer version of the built-in extractors.
const producerVersion = "0.2.0"

// skipDir reports directories no extractor here reads: VCS and tool state,
// dependencies and build output.
func skipDir(name string) bool {
	switch name {
	case ".git", ".github", ".waxseal", ".cartographer", "node_modules", "deps", "_build", "vendor", "target", "dist":
		return true
	}
	return false
}

// yamlFiles lists root's .yaml/.yml files, relative and slash-separated, sorted.
func yamlFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p != root && skipDir(e.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext == ".yaml" || ext == ".yml" {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// docs decodes every YAML document in a file; a file that doesn't parse
// yields what parsed before the error.
func yamlDocs(path string) []*yaml.Node {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) || err != nil {
			return out
		}
		if len(n.Content) > 0 {
			out = append(out, n.Content[0])
		}
	}
}

// get walks mapping keys; nil when a step is missing.
func get(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == k {
				next = n.Content[i+1]
				break
			}
		}
		n = next
	}
	return n
}

func str(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func strMap(n *yaml.Node) map[string]string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	m := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if v := str(n.Content[i+1]); v != "" {
			m[n.Content[i].Value] = v
		}
	}
	return m
}

// Kubernetes reads Deployments, StatefulSets and Services from plain
// manifests under root: name, namespace, container images, pod labels,
// literal env values that are URLs, and Service selectors.
func Kubernetes(repo, root, revision string) (*facts.Doc, error) {
	d := &facts.Doc{FactsVersion: facts.Version, Producer: facts.Producer{Name: "cartographer-kubernetes", Version: producerVersion}, Repo: repo, Revision: revision}
	files, err := yamlFiles(root)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		listed := false
		for _, n := range yamlDocs(filepath.Join(root, filepath.FromSlash(rel))) {
			kind, name := str(get(n, "kind")), str(get(n, "metadata", "name"))
			if name == "" {
				continue
			}
			space := str(get(n, "metadata", "namespace"))
			switch kind {
			case "Deployment", "StatefulSet":
				labels := strMap(get(n, "spec", "template", "metadata", "labels"))
				containers := get(n, "spec", "template", "spec", "containers")
				if containers == nil || containers.Kind != yaml.SequenceNode {
					continue
				}
				for _, c := range containers.Content {
					env := map[string]string{}
					if list := get(c, "env"); list != nil && list.Kind == yaml.SequenceNode {
						for _, e := range list.Content {
							// Only URLs: they are what links services, and
							// nothing else from a manifest belongs in a map.
							if k, v := str(get(e, "name")), str(get(e, "value")); k != "" && isURL(v) {
								env[k] = strings.TrimSpace(v)
							}
						}
					}
					if len(env) == 0 {
						env = nil
					}
					d.Services = append(d.Services, facts.Service{Kind: "deployment", Name: name, Namespace: space, Image: str(get(c, "image")),
						Labels: labels, Env: env, File: rel, Line: n.Line})
					listed = true
				}
			case "Service":
				d.Services = append(d.Services, facts.Service{Kind: "service", Name: name, Namespace: space,
					Selector: strMap(get(n, "spec", "selector")), File: rel, Line: n.Line})
				listed = true
			}
		}
		if listed {
			d.Files = append(d.Files, facts.File{Path: rel, Language: "yaml", Category: "manifest"})
		}
	}
	return d, d.Validate()
}

// isURL reports an http(s) URL with a host.
func isURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) && len(s) > len("https://")
}
