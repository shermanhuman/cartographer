package extract

import (
	"strings"

	"github.com/shermanhuman/cartographer/internal/facts"
	"go.yaml.in/yaml/v3"
)

// ConfigURLs lists http(s) URLs in a repository's YAML configuration, keyed
// by their dotted path (cold.admin_url). Kubernetes manifests are skipped:
// their env values are read by Kubernetes. Only literal values count.
func ConfigURLs(repo, root, revision string) (*facts.Doc, error) {
	d := &facts.Doc{FactsVersion: facts.Version, Producer: facts.Producer{Name: "cartographer-config-urls", Version: producerVersion}, Repo: repo, Revision: revision}
	files, err := yamlFiles(root)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		found := false
		docs, notes := yamlDocs(root, rel)
		d.Notes = append(d.Notes, notes...)
		for _, n := range docs {
			if str(get(n, "kind")) != "" && str(get(n, "apiVersion")) != "" {
				continue // a manifest
			}
			walkURLs(n, "", func(key, value string, line int) {
				d.ConfigURLs = append(d.ConfigURLs, facts.ConfigURL{Key: key, Value: strings.TrimSpace(value), File: rel, Line: line})
				found = true
			})
		}
		if found {
			d.Files = append(d.Files, facts.File{Path: rel, Language: "yaml", Category: "config"})
		}
	}
	return d, d.Validate()
}

func walkURLs(n *yaml.Node, prefix string, emit func(key, value string, line int)) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if prefix != "" {
				k = prefix + "." + k
			}
			walkURLs(n.Content[i+1], k, emit)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkURLs(c, prefix+"[]", emit)
		}
	case yaml.ScalarNode:
		if prefix != "" && isURL(n.Value) {
			emit(prefix, n.Value, n.Line)
		}
	}
}
