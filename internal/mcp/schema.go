package mcp

import (
	"fmt"
	"strings"
)

type fieldSpec struct {
	kind                string
	nullable            bool
	hasDefault          bool
	defaultValue        any
	minimum             *float64
	itemKind            string
	additionalValueKind string
}

type toolSpec struct {
	name        string
	mode        string
	required    []string
	fields      map[string]fieldSpec
	destructive bool
}

// Tool is the MCP tools/list representation.
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  map[string]any `json:"annotations,omitempty"`
}

var readSpecs = []toolSpec{
	read("get_board", required("board_id"), fields(
		"board_id", integer(), "include_cards", booleanDefault(false))),
	read("get_board_structure", required("board"), fields("board", stringField())),
	read("get_card", required("card_id"), fields(
		"card_id", integer(), "include_comments", booleanDefault(false), "include_members", booleanDefault(true), "include_relations", booleanDefault(true))),
	read("get_card_checklists", required("card_id"), fields("card_id", integer())),
	read("get_card_children", required("card_id"), fields("card_id", integer())),
	read("get_current_user", nil, fields()),
	read("get_member_cards", required("user"), mergeFields(fields("user", stringField()), paginationFields())),
	read("get_my_cards", nil, paginationFields()),
	read("get_responsible_cards", required("user"), mergeFields(fields("user", stringField()), paginationFields())),
	read("get_server_info", nil, fields()),
	read("get_space", required("space_id"), fields("space_id", integer())),
	read("list_boards", nil, fields("space_id", nullableDefault(integer()), "limit", nullableNonNegativeInteger())),
	read("list_card_types", nil, fields()),
	read("list_custom_properties", nil, fields()),
	read("list_spaces", nil, fields()),
	read("list_tags", nil, fields()),
	read("list_users", nil, fields("space_id", nullableDefault(integer()), "limit", nullableNonNegativeInteger(), "skip", nullableNonNegativeInteger())),
	read("search_cards", nil, mergeFields(
		fields(
			"query", nullableDefault(stringField()),
			"board_id", nullableDefault(integer()),
			"space_id", nullableDefault(integer()),
			"limit", nullableNonNegativeInteger(),
			"skip", nullableNonNegativeInteger(),
			"include_archived", booleanDefault(false),
			"fetch_all", booleanDefault(false),
		),
	)),
}

var writeSpecs = []toolSpec{
	write("add_checklist_item", required("card_id", "checklist_id", "text"), fields("card_id", integer(), "checklist_id", integer(), "text", stringField()), false),
	write("add_comment", required("card_id", "text"), fields("card_id", integer(), "text", stringField()), false),
	write("add_external_link", required("card_id", "url"), fields("card_id", integer(), "url", stringField(), "description", nullableDefault(stringField())), false),
	write("add_watcher", required("card_id", "user"), fields("card_id", integer(), "user", stringField()), false),
	write("create_card", required("title", "board", "column"), fields(
		"title", stringField(), "board", stringField(), "column", stringField(),
		"lane_id", nullableDefault(integer()), "type_id", nullableDefault(integer()), "description", nullableDefault(stringField()),
		"owner", nullableDefault(stringField()), "due_date", nullableDefault(stringField()), "size_text", nullableDefault(stringField()),
		"planned_start", nullableDefault(stringField()), "planned_end", nullableDefault(stringField()),
		"properties", nullableDefault(stringMap()), "tag_ids", nullableDefault(integerArray())), false),
	write("create_checklist", required("card_id", "name"), fields("card_id", integer(), "name", stringField()), false),
	write("delete_checklist", required("card_id", "checklist_id"), fields("card_id", integer(), "checklist_id", integer()), true),
	write("delete_checklist_item", required("card_id", "checklist_id", "item_id"), fields("card_id", integer(), "checklist_id", integer(), "item_id", integer()), true),
	write("link_child_card", required("parent_card_id", "child_card_id"), fields("parent_card_id", integer(), "child_card_id", integer()), false),
	write("move_card", required("card_id", "column"), fields("card_id", integer(), "column", stringField(), "board", nullableDefault(stringField()), "lane_id", nullableDefault(integer())), false),
	write("remove_member", required("card_id", "user"), fields("card_id", integer(), "user", stringField()), true),
	write("set_responsible", required("card_id", "user"), fields("card_id", integer(), "user", stringField()), false),
	write("unlink_child_card", required("parent_card_id", "child_card_id"), fields("parent_card_id", integer(), "child_card_id", integer()), true),
	write("update_card", required("card_id"), fields(
		"card_id", integer(), "title", nullableDefault(stringField()), "description", nullableDefault(stringField()), "due_date", nullableDefault(stringField()),
		"owner", nullableDefault(stringField()), "type_id", nullableDefault(integer()), "size_text", nullableDefault(stringField()),
		"planned_start", nullableDefault(stringField()), "planned_end", nullableDefault(stringField()),
		"properties", nullableDefault(stringMap()), "tag_ids", nullableDefault(integerArray())), false),
	write("update_checklist_item", required("card_id", "checklist_id", "item_id"), fields(
		"card_id", integer(), "checklist_id", integer(), "item_id", integer(), "text", nullableDefault(stringField()), "checked", nullableDefault(boolean())), false),
}

func read(name string, required []string, fields map[string]fieldSpec) toolSpec {
	return toolSpec{name: name, mode: "read", required: required, fields: fields}
}

func write(name string, required []string, fields map[string]fieldSpec, destructive bool) toolSpec {
	return toolSpec{name: name, mode: "write", required: required, fields: fields, destructive: destructive}
}

func required(names ...string) []string { return names }

func fields(values ...any) map[string]fieldSpec {
	result := make(map[string]fieldSpec, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		result[values[index].(string)] = values[index+1].(fieldSpec)
	}
	return result
}

func mergeFields(groups ...map[string]fieldSpec) map[string]fieldSpec {
	result := make(map[string]fieldSpec)
	for _, group := range groups {
		for name, spec := range group {
			result[name] = spec
		}
	}
	return result
}

func paginationFields() map[string]fieldSpec {
	return fields("limit", nullableNonNegativeInteger(), "skip", nullableNonNegativeInteger(), "fetch_all", booleanDefault(false))
}

func integer() fieldSpec      { return fieldSpec{kind: "integer"} }
func stringField() fieldSpec  { return fieldSpec{kind: "string"} }
func boolean() fieldSpec      { return fieldSpec{kind: "boolean"} }
func stringMap() fieldSpec    { return fieldSpec{kind: "object", additionalValueKind: "string"} }
func integerArray() fieldSpec { return fieldSpec{kind: "array", itemKind: "integer"} }

func nullableDefault(base fieldSpec) fieldSpec {
	base.nullable = true
	base.hasDefault = true
	base.defaultValue = nil
	return base
}

func nullableNonNegativeInteger() fieldSpec {
	minimum := float64(0)
	return fieldSpec{kind: "integer", nullable: true, hasDefault: true, defaultValue: nil, minimum: &minimum}
}

func booleanDefault(value bool) fieldSpec {
	return fieldSpec{kind: "boolean", hasDefault: true, defaultValue: value}
}

func tools(writeEnabled bool) []Tool {
	specs := append([]toolSpec(nil), readSpecs...)
	if writeEnabled {
		specs = append(specs, writeSpecs...)
	}
	result := make([]Tool, 0, len(specs))
	for _, spec := range specs {
		properties := make(map[string]any, len(spec.fields))
		for name, field := range spec.fields {
			properties[name] = field.schema()
		}
		annotations := map[string]any{
			"title":           humanTitle(spec.name),
			"readOnlyHint":    spec.mode == "read",
			"destructiveHint": spec.destructive,
			"idempotentHint":  spec.mode == "read",
			"openWorldHint":   true,
		}
		result = append(result, Tool{
			Name:         spec.name,
			Description:  fmt.Sprintf("%s in the configured Kaiten workspace.", humanTitle(spec.name)),
			InputSchema:  map[string]any{"type": "object", "required": nonNilSlice(spec.required), "properties": properties, "additionalProperties": false},
			OutputSchema: resultSchema(), Annotations: annotations,
		})
	}
	return result
}

func (f fieldSpec) schema() map[string]any {
	typeValue := any(f.kind)
	if f.nullable {
		typeValue = []string{f.kind, "null"}
	}
	result := map[string]any{"type": typeValue}
	if f.hasDefault {
		result["default"] = f.defaultValue
	}
	if f.minimum != nil {
		result["minimum"] = *f.minimum
	}
	if f.itemKind != "" {
		result["items"] = map[string]any{"type": f.itemKind}
	}
	if f.additionalValueKind != "" {
		result["additionalProperties"] = map[string]any{"type": f.additionalValueKind}
	}
	return result
}

func resultSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object", "required": []string{"ok", "data", "meta"},
				"properties": map[string]any{
					"ok": map[string]any{"type": "boolean", "const": true}, "data": map[string]any{},
					"meta": map[string]any{"type": "object", "required": []string{"source", "cached"}, "properties": map[string]any{
						"source": map[string]any{"type": "string", "const": "kaiten"}, "cached": map[string]any{"type": "boolean"},
					}},
				},
			},
			map[string]any{
				"type": "object", "required": []string{"ok", "error"},
				"properties": map[string]any{
					"ok": map[string]any{"type": "boolean", "const": false},
					"error": map[string]any{"type": "object", "required": []string{"type", "message"}, "properties": map[string]any{
						"type":    map[string]any{"type": "string", "enum": []string{"auth", "not_found", "rate_limit", "validation", "upstream"}},
						"message": map[string]any{"type": "string"}, "status_code": map[string]any{"type": []string{"integer", "null"}},
					}},
				},
			},
		},
	}
}

func findToolSpec(name string, writeEnabled bool) (toolSpec, bool) {
	for _, spec := range readSpecs {
		if spec.name == name {
			return spec, true
		}
	}
	if writeEnabled {
		for _, spec := range writeSpecs {
			if spec.name == name {
				return spec, true
			}
		}
	}
	return toolSpec{}, false
}

func humanTitle(name string) string {
	words := strings.Fields(strings.ReplaceAll(name, "_", " "))
	for index := range words {
		words[index] = strings.ToUpper(words[index][:1]) + words[index][1:]
	}
	return strings.Join(words, " ")
}

func nonNilSlice(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}
