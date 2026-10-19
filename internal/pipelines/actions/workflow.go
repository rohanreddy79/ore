// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is the YAML model of a workflow file. The polymorphic parts of
// the syntax (`on` as a string, a list or a map; `runs-on` as a string or a
// list; `needs`; `matrix` with include/exclude; `continue-on-error` as a
// bool or an expression) are decoded from yaml.Node so that job and step
// order is preserved and unknown keys can be reported.

// Workflow is one workflow file.
type Workflow struct {
	Name        string
	RunName     string
	On          []EventSpec
	Env         map[string]string
	Defaults    Defaults
	Concurrency *ConcurrencySpec
	Permissions map[string]string
	Jobs        []JobEntry
	Unknown     []string
}

// JobEntry keeps the file order of jobs.
type JobEntry struct {
	ID  string
	Job Job
}

// EventSpec is one entry of `on`.
type EventSpec struct {
	Name           string
	Branches       []string
	BranchesIgnore []string
	Tags           []string
	TagsIgnore     []string
	Paths          []string
	PathsIgnore    []string
	Types          []string
	Cron           []string
	Inputs         []InputEntry
	Unknown        []string
}

// InputEntry is a workflow_dispatch input, in file order.
type InputEntry struct {
	Name        string
	Description string
	Type        string
	Default     any
	Required    bool
	Options     []any
}

// Defaults are `defaults.run`.
type Defaults struct {
	Shell            string
	WorkingDirectory string
}

// ConcurrencySpec is `concurrency`.
type ConcurrencySpec struct {
	Group            string
	CancelInProgress bool
}

// Job is one entry of `jobs`.
type Job struct {
	Name            string
	RunsOn          []string
	Needs           []string
	If              string
	Env             map[string]string
	Defaults        Defaults
	Environment     string
	EnvironmentURL  string
	Outputs         []KV
	Strategy        *Strategy
	TimeoutMinutes  float64
	ContinueOnError StrOrBool
	Container       *ContainerSpec
	Services        []ServiceSpec
	Steps           []Step
	Uses            string
	With            map[string]string
	SecretsInherit  bool
	Secrets         map[string]string
	Permissions     map[string]string
	Concurrency     *ConcurrencySpec
	Unknown         []string
}

// KV is an ordered key/value pair.
type KV struct {
	Key, Value string
}

// Strategy is `strategy`.
type Strategy struct {
	Matrix      *MatrixSpec
	MatrixExpr  string
	FailFast    *bool
	MaxParallel int
}

// MatrixSpec is `strategy.matrix`.
type MatrixSpec struct {
	Axes    []Axis
	Include []map[string]any
	Exclude []map[string]any
	// DynamicAxes are axes whose value is an expression (fromJSON(...)).
	DynamicAxes []string
}

// Axis is one matrix dimension in declaration order.
type Axis struct {
	Name   string
	Values []any
}

// ContainerSpec is `jobs.<id>.container`.
type ContainerSpec struct {
	Image          string
	Env            map[string]string
	Options        string
	HasCredentials bool
	Ports          []string
	Volumes        []string
}

// ServiceSpec is one of `jobs.<id>.services`.
type ServiceSpec struct {
	Name    string
	Image   string
	Env     map[string]string
	Ports   []string
	Options string
}

// Step is one of `jobs.<id>.steps`.
type Step struct {
	ID               string
	Name             string
	If               string
	Uses             string
	Run              string
	Shell            string
	WorkingDirectory string
	With             []KV
	Env              map[string]string
	ContinueOnError  StrOrBool
	TimeoutMinutes   float64
	Unknown          []string
}

// StrOrBool is a field that may be a bool or an expression string.
type StrOrBool struct {
	Set  bool
	Bool bool
	Expr string
}

// Parse decodes a workflow file.
func Parse(src []byte) (*Workflow, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("yaml: empty document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yaml: the workflow must be a mapping")
	}
	w := &Workflow{Env: map[string]string{}}
	for _, kv := range mapPairs(root) {
		switch kv.key {
		case "name":
			w.Name = kv.val.Value
		case "run-name":
			w.RunName = kv.val.Value
		case "on":
			on, err := parseOn(kv.val)
			if err != nil {
				return nil, err
			}
			w.On = on
		case "env":
			w.Env = stringMap(kv.val)
		case "defaults":
			w.Defaults = parseDefaults(kv.val)
		case "concurrency":
			w.Concurrency = parseConcurrency(kv.val)
		case "permissions":
			w.Permissions = parsePermissions(kv.val)
		case "jobs":
			if kv.val.Kind != yaml.MappingNode && !(kv.val.Kind == yaml.ScalarNode && kv.val.Tag == "!!null") {
				return nil, fmt.Errorf("jobs: must be a mapping")
			}
			for _, j := range mapPairs(kv.val) {
				job, err := parseJob(j.key, j.val)
				if err != nil {
					return nil, err
				}
				w.Jobs = append(w.Jobs, JobEntry{ID: j.key, Job: job})
			}
		default:
			w.Unknown = append(w.Unknown, kv.key)
		}
	}
	return w, nil
}

type pair struct {
	key string
	val *yaml.Node
}

func mapPairs(n *yaml.Node) []pair {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]pair, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, pair{n.Content[i].Value, n.Content[i+1]})
	}
	return out
}

func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// stringList accepts a scalar or a sequence of scalars.
func stringList(n *yaml.Node) []string {
	if isNull(n) {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		return out
	}
	return nil
}

func stringMap(n *yaml.Node) map[string]string {
	out := map[string]string{}
	for _, kv := range mapPairs(n) {
		out[kv.key] = scalarText(kv.val)
	}
	return out
}

func scalarText(n *yaml.Node) string {
	if isNull(n) {
		return ""
	}
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	b, _ := yaml.Marshal(n)
	return strings.TrimSpace(string(b))
}

// anyValue decodes a node into JSON-like values (numbers as float64).
func anyValue(n *yaml.Node) any {
	if isNull(n) {
		return nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return n.Value
	}
	return normalize(v)
}

func normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case float32:
		return float64(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	}
	return v
}

func parseFloat(n *yaml.Node) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(n.Value), 64)
	return f
}

func parseBool(n *yaml.Node) bool {
	return strings.EqualFold(strings.TrimSpace(n.Value), "true")
}

func parseStrOrBool(n *yaml.Node) StrOrBool {
	v := strings.TrimSpace(n.Value)
	switch strings.ToLower(v) {
	case "true":
		return StrOrBool{Set: true, Bool: true}
	case "false", "":
		return StrOrBool{Set: true, Bool: false}
	}
	return StrOrBool{Set: true, Expr: v}
}

func parseOn(n *yaml.Node) ([]EventSpec, error) {
	var out []EventSpec
	switch n.Kind {
	case yaml.ScalarNode:
		for _, name := range strings.Fields(strings.ReplaceAll(n.Value, ",", " ")) {
			out = append(out, EventSpec{Name: name})
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, EventSpec{Name: c.Value})
		}
	case yaml.MappingNode:
		for _, kv := range mapPairs(n) {
			ev := EventSpec{Name: kv.key}
			if kv.key == "schedule" {
				for _, item := range kv.val.Content {
					for _, p := range mapPairs(item) {
						if p.key == "cron" {
							ev.Cron = append(ev.Cron, p.val.Value)
						}
					}
				}
				out = append(out, ev)
				continue
			}
			for _, p := range mapPairs(kv.val) {
				switch p.key {
				case "branches":
					ev.Branches = stringList(p.val)
				case "branches-ignore":
					ev.BranchesIgnore = stringList(p.val)
				case "tags":
					ev.Tags = stringList(p.val)
				case "tags-ignore":
					ev.TagsIgnore = stringList(p.val)
				case "paths":
					ev.Paths = stringList(p.val)
				case "paths-ignore":
					ev.PathsIgnore = stringList(p.val)
				case "types":
					ev.Types = stringList(p.val)
				case "inputs":
					for _, in := range mapPairs(p.val) {
						ie := InputEntry{Name: in.key}
						for _, f := range mapPairs(in.val) {
							switch f.key {
							case "description":
								ie.Description = f.val.Value
							case "type":
								ie.Type = f.val.Value
							case "default":
								ie.Default = anyValue(f.val)
							case "required":
								ie.Required = parseBool(f.val)
							case "options":
								if l, ok := anyValue(f.val).([]any); ok {
									ie.Options = l
								}
							}
						}
						ev.Inputs = append(ev.Inputs, ie)
					}
				default:
					ev.Unknown = append(ev.Unknown, p.key)
				}
			}
			// `!pattern` entries are ignores
			ev.Branches, ev.BranchesIgnore = splitNegations(ev.Branches, ev.BranchesIgnore)
			ev.Tags, ev.TagsIgnore = splitNegations(ev.Tags, ev.TagsIgnore)
			ev.Paths, ev.PathsIgnore = splitNegations(ev.Paths, ev.PathsIgnore)
			out = append(out, ev)
		}
	default:
		return nil, fmt.Errorf("on: must be a string, a list or a mapping")
	}
	return out, nil
}

func splitNegations(include, ignore []string) ([]string, []string) {
	var keep []string
	for _, p := range include {
		if strings.HasPrefix(p, "!") {
			ignore = append(ignore, strings.TrimPrefix(p, "!"))
		} else {
			keep = append(keep, p)
		}
	}
	return keep, ignore
}

func parseDefaults(n *yaml.Node) Defaults {
	var d Defaults
	for _, kv := range mapPairs(n) {
		if kv.key == "run" {
			for _, p := range mapPairs(kv.val) {
				switch p.key {
				case "shell":
					d.Shell = p.val.Value
				case "working-directory":
					d.WorkingDirectory = p.val.Value
				}
			}
		}
	}
	return d
}

func parseConcurrency(n *yaml.Node) *ConcurrencySpec {
	if isNull(n) {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		return &ConcurrencySpec{Group: n.Value}
	}
	c := &ConcurrencySpec{}
	for _, kv := range mapPairs(n) {
		switch kv.key {
		case "group":
			c.Group = kv.val.Value
		case "cancel-in-progress":
			c.CancelInProgress = parseBool(kv.val)
		}
	}
	return c
}

func parsePermissions(n *yaml.Node) map[string]string {
	if isNull(n) {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		switch n.Value {
		case "read-all":
			return map[string]string{"all": "read"}
		case "write-all":
			return map[string]string{"all": "write"}
		}
		return map[string]string{"all": n.Value}
	}
	return stringMap(n)
}

func parseJob(id string, n *yaml.Node) (Job, error) {
	j := Job{Env: map[string]string{}}
	if n.Kind != yaml.MappingNode {
		return j, fmt.Errorf("jobs.%s: must be a mapping", id)
	}
	for _, kv := range mapPairs(n) {
		switch kv.key {
		case "name":
			j.Name = kv.val.Value
		case "runs-on":
			switch kv.val.Kind {
			case yaml.MappingNode:
				for _, p := range mapPairs(kv.val) {
					if p.key == "labels" {
						j.RunsOn = append(j.RunsOn, stringList(p.val)...)
					}
					if p.key == "group" {
						j.RunsOn = append(j.RunsOn, "group:"+p.val.Value)
					}
				}
			default:
				j.RunsOn = stringList(kv.val)
			}
		case "needs":
			j.Needs = stringList(kv.val)
		case "if":
			j.If = kv.val.Value
		case "env":
			j.Env = stringMap(kv.val)
		case "defaults":
			j.Defaults = parseDefaults(kv.val)
		case "environment":
			if kv.val.Kind == yaml.MappingNode {
				for _, p := range mapPairs(kv.val) {
					switch p.key {
					case "name":
						j.Environment = p.val.Value
					case "url":
						j.EnvironmentURL = p.val.Value
					}
				}
			} else {
				j.Environment = kv.val.Value
			}
		case "outputs":
			for _, p := range mapPairs(kv.val) {
				j.Outputs = append(j.Outputs, KV{p.key, scalarText(p.val)})
			}
		case "strategy":
			j.Strategy = parseStrategy(kv.val)
		case "timeout-minutes":
			j.TimeoutMinutes = parseFloat(kv.val)
		case "continue-on-error":
			j.ContinueOnError = parseStrOrBool(kv.val)
		case "container":
			j.Container = parseContainer(kv.val)
		case "services":
			for _, p := range mapPairs(kv.val) {
				c := parseContainer(p.val)
				s := ServiceSpec{Name: p.key}
				if c != nil {
					s.Image, s.Env, s.Ports, s.Options = c.Image, c.Env, c.Ports, c.Options
				}
				j.Services = append(j.Services, s)
			}
		case "steps":
			for i, sn := range kv.val.Content {
				st, err := parseStep(id, i, sn)
				if err != nil {
					return j, err
				}
				j.Steps = append(j.Steps, st)
			}
		case "uses":
			j.Uses = kv.val.Value
		case "with":
			j.With = stringMap(kv.val)
		case "secrets":
			if kv.val.Kind == yaml.ScalarNode && kv.val.Value == "inherit" {
				j.SecretsInherit = true
			} else {
				j.Secrets = stringMap(kv.val)
			}
		case "permissions":
			j.Permissions = parsePermissions(kv.val)
		case "concurrency":
			j.Concurrency = parseConcurrency(kv.val)
		default:
			j.Unknown = append(j.Unknown, kv.key)
		}
	}
	return j, nil
}

func parseContainer(n *yaml.Node) *ContainerSpec {
	if isNull(n) {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		return &ContainerSpec{Image: n.Value}
	}
	c := &ContainerSpec{Env: map[string]string{}}
	for _, kv := range mapPairs(n) {
		switch kv.key {
		case "image":
			c.Image = kv.val.Value
		case "env":
			c.Env = stringMap(kv.val)
		case "options":
			c.Options = kv.val.Value
		case "credentials":
			c.HasCredentials = true
		case "ports":
			c.Ports = stringList(kv.val)
		case "volumes":
			c.Volumes = stringList(kv.val)
		}
	}
	return c
}

func parseStrategy(n *yaml.Node) *Strategy {
	s := &Strategy{}
	for _, kv := range mapPairs(n) {
		switch kv.key {
		case "fail-fast":
			b := parseBool(kv.val)
			s.FailFast = &b
		case "max-parallel":
			s.MaxParallel, _ = strconv.Atoi(kv.val.Value)
		case "matrix":
			if kv.val.Kind == yaml.ScalarNode {
				s.MatrixExpr = kv.val.Value
				continue
			}
			m := &MatrixSpec{}
			for _, p := range mapPairs(kv.val) {
				switch p.key {
				case "include", "exclude":
					var list []map[string]any
					if p.val.Kind == yaml.ScalarNode {
						// include: ${{ fromJSON(...) }}
						m.DynamicAxes = append(m.DynamicAxes, p.key)
						continue
					}
					for _, item := range p.val.Content {
						entry := map[string]any{}
						for _, f := range mapPairs(item) {
							entry[f.key] = anyValue(f.val)
						}
						list = append(list, entry)
					}
					if p.key == "include" {
						m.Include = list
					} else {
						m.Exclude = list
					}
				default:
					if p.val.Kind == yaml.ScalarNode {
						m.DynamicAxes = append(m.DynamicAxes, p.key)
						m.Axes = append(m.Axes, Axis{Name: p.key, Values: []any{p.val.Value}})
						continue
					}
					vals, _ := anyValue(p.val).([]any)
					m.Axes = append(m.Axes, Axis{Name: p.key, Values: vals})
				}
			}
			s.Matrix = m
		}
	}
	return s
}

func parseStep(job string, i int, n *yaml.Node) (Step, error) {
	st := Step{Env: map[string]string{}}
	if n.Kind != yaml.MappingNode {
		return st, fmt.Errorf("jobs.%s.steps[%d]: must be a mapping", job, i)
	}
	for _, kv := range mapPairs(n) {
		switch kv.key {
		case "id":
			st.ID = kv.val.Value
		case "name":
			st.Name = kv.val.Value
		case "if":
			st.If = kv.val.Value
		case "uses":
			st.Uses = kv.val.Value
		case "run":
			st.Run = kv.val.Value
		case "shell":
			st.Shell = kv.val.Value
		case "working-directory":
			st.WorkingDirectory = kv.val.Value
		case "with":
			for _, p := range mapPairs(kv.val) {
				st.With = append(st.With, KV{p.key, scalarText(p.val)})
			}
		case "env":
			st.Env = stringMap(kv.val)
		case "continue-on-error":
			st.ContinueOnError = parseStrOrBool(kv.val)
		case "timeout-minutes":
			st.TimeoutMinutes = parseFloat(kv.val)
		default:
			st.Unknown = append(st.Unknown, kv.key)
		}
	}
	if st.Uses == "" && st.Run == "" {
		return st, fmt.Errorf("jobs.%s.steps[%d]: a step needs `run` or `uses`", job, i)
	}
	if st.Uses != "" && st.Run != "" {
		return st, fmt.Errorf("jobs.%s.steps[%d]: a step cannot have both `run` and `uses`", job, i)
	}
	return st, nil
}

// sortedKeys returns a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
