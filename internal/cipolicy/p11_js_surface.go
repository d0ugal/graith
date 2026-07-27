package cipolicy

import (
	"fmt"
	"os"
	"sort"

	yaml "go.yaml.in/yaml/v3"
)

type P11WorkflowSummary struct {
	Name                  string
	Events                []string
	Permissions           map[string]string
	PermissionsExpression string
	Env                   map[string]string
	Jobs                  map[string]P11WorkflowJob
	Scalars               []string
}

type P11WorkflowJob struct {
	Name                  string
	If                    string
	Needs                 []string
	RunsOn                string
	Permissions           map[string]string
	PermissionsExpression string
	Env                   map[string]string
	Steps                 []P11WorkflowStep
}

type P11WorkflowStep struct {
	Name string
	Uses string
	If   string
	Env  map[string]string
	With map[string]string
	Run  string
}

func ReadP11WorkflowSummary(path string) (P11WorkflowSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return P11WorkflowSummary{}, err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return P11WorkflowSummary{}, fmt.Errorf("decode workflow %s: %w", path, err)
	}

	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return P11WorkflowSummary{}, fmt.Errorf("workflow %s is not a mapping", path)
	}

	node := root.Content[0]
	permissions, permissionsExpression := p11StringMapOrExpression(p11MappingValue(node, "permissions"))
	summary := P11WorkflowSummary{
		Name:                  p11Scalar(p11MappingValue(node, "name")),
		Events:                p11EventNames(p11MappingValue(node, "on")),
		Permissions:           permissions,
		PermissionsExpression: permissionsExpression,
		Env:                   p11StringMap(p11MappingValue(node, "env")),
		Jobs:                  map[string]P11WorkflowJob{},
		Scalars:               p11ScalarValues(node),
	}

	jobs := p11MappingValue(node, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return P11WorkflowSummary{}, fmt.Errorf("workflow %s has no jobs mapping", path)
	}

	for index := 0; index < len(jobs.Content); index += 2 {
		id := jobs.Content[index].Value
		body := jobs.Content[index+1]

		if body.Kind != yaml.MappingNode {
			return P11WorkflowSummary{}, fmt.Errorf("workflow job %s is not a mapping", id)
		}

		permissions, permissionsExpression := p11StringMapOrExpression(p11MappingValue(body, "permissions"))
		summary.Jobs[id] = P11WorkflowJob{
			Name:                  p11Scalar(p11MappingValue(body, "name")),
			If:                    p11Scalar(p11MappingValue(body, "if")),
			Needs:                 p11StringList(p11MappingValue(body, "needs")),
			RunsOn:                p11Scalar(p11MappingValue(body, "runs-on")),
			Permissions:           permissions,
			PermissionsExpression: permissionsExpression,
			Env:                   p11StringMap(p11MappingValue(body, "env")),
			Steps:                 p11Steps(p11MappingValue(body, "steps")),
		}
	}

	return summary, nil
}

func p11MappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}

	return nil
}

func p11Scalar(node *yaml.Node) string {
	if node == nil {
		return ""
	}

	return node.Value
}

func p11ScalarValues(node *yaml.Node) []string {
	var values []string

	var walk func(*yaml.Node, map[*yaml.Node]bool)

	walk = func(current *yaml.Node, resolvingAliases map[*yaml.Node]bool) {
		if current == nil {
			return
		}

		if current.Kind == yaml.AliasNode {
			if current.Alias == nil || resolvingAliases[current.Alias] {
				return
			}

			resolvingAliases[current.Alias] = true
			walk(current.Alias, resolvingAliases)
			delete(resolvingAliases, current.Alias)

			return
		}

		if current.Kind == yaml.ScalarNode {
			values = append(values, current.Value)
		}

		for _, child := range current.Content {
			walk(child, resolvingAliases)
		}
	}

	walk(node, map[*yaml.Node]bool{})

	return values
}

func p11StringMap(node *yaml.Node) map[string]string {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	result := map[string]string{}
	for index := 0; index < len(node.Content); index += 2 {
		result[node.Content[index].Value] = node.Content[index+1].Value
	}

	return result
}

func p11StringMapOrExpression(node *yaml.Node) (map[string]string, string) {
	if node == nil {
		return nil, ""
	}

	if node.Kind != yaml.MappingNode {
		if node.Value != "" {
			return nil, node.Value
		}

		return nil, node.ShortTag()
	}

	return p11StringMap(node), ""
}

func p11StringList(node *yaml.Node) []string {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return nil
		}

		return []string{node.Value}
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			values = append(values, item.Value)
		}

		return values
	default:
		return nil
	}
}

func p11EventNames(node *yaml.Node) []string {
	if node == nil {
		return nil
	}

	var events []string

	switch node.Kind {
	case yaml.ScalarNode:
		events = append(events, node.Value)
	case yaml.SequenceNode:
		for _, event := range node.Content {
			events = append(events, event.Value)
		}
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			events = append(events, node.Content[index].Value)
		}
	}

	sort.Strings(events)

	return events
}

func p11Steps(node *yaml.Node) []P11WorkflowStep {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}

	steps := make([]P11WorkflowStep, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}

		steps = append(steps, P11WorkflowStep{
			Name: p11Scalar(p11MappingValue(item, "name")),
			Uses: p11Scalar(p11MappingValue(item, "uses")),
			If:   p11Scalar(p11MappingValue(item, "if")),
			Env:  p11StringMap(p11MappingValue(item, "env")),
			With: p11StringMap(p11MappingValue(item, "with")),
			Run:  p11Scalar(p11MappingValue(item, "run")),
		})
	}

	return steps
}
