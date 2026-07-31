// Package cli implements the automation-oriented kaiten command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/api"
	"github.com/dsuranov/kaiten-mcp/internal/config"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

// Dependencies supplies lifecycle behavior shared with kaiten-mcp.
type Dependencies struct {
	MCPRun       func(context.Context, []string, io.Reader, io.Writer, io.Writer) int
	MCPInstall   func(context.Context, io.Reader, io.Writer, io.Writer) int
	MCPUninstall func(context.Context, io.Reader, io.Writer, io.Writer) int
}

type flagDef struct {
	name     string
	value    string
	boolean  bool
	required bool
}

type commandSpec struct {
	group, name string
	usage       string
	positionals int
	flags       []flagDef
}

var commandSpecs = []commandSpec{
	{group: "spaces", name: "list", usage: "kaiten spaces list"},
	{group: "spaces", name: "get", usage: "kaiten spaces get <space_id>", positionals: 1},
	{group: "boards", name: "list", usage: "kaiten boards list --space-id <id>", flags: []flagDef{valueFlag("space-id", "id", true)}},
	{group: "boards", name: "get", usage: "kaiten boards get <board_id>", positionals: 1},
	{group: "columns", name: "list", usage: "kaiten columns list --board-id <id>", flags: []flagDef{valueFlag("board-id", "id", true)}},
	{group: "lanes", name: "list", usage: "kaiten lanes list --board-id <id>", flags: []flagDef{valueFlag("board-id", "id", true)}},
	{group: "cards", name: "list", usage: "kaiten cards list --board-id <id> [--archived|--all]", flags: []flagDef{valueFlag("board-id", "id", true), boolFlag("archived"), boolFlag("all")}},
	{group: "cards", name: "get", usage: "kaiten cards get <card_id>", positionals: 1},
	{group: "cards", name: "create", usage: "kaiten cards create --title <text> --board-id <id> (--column-id <id>|--column-name <text>) [flags]", flags: []flagDef{
		valueFlag("title", "text", true), valueFlag("board-id", "id", true), valueFlag("column-id", "id", false), valueFlag("column-name", "text", false), valueFlag("lane-id", "id", false), valueFlag("lane-name", "text", false), valueFlag("type-id", "id", false), valueFlag("position", "1|2", false), valueFlag("description", "text", false), valueFlag("size", "positive-int", false), valueFlag("due-date", "iso-8601", false),
	}},
	{group: "cards", name: "update", usage: "kaiten cards update <card_id> [flags]", positionals: 1, flags: []flagDef{
		valueFlag("board-id", "id", false), valueFlag("title", "text", false), valueFlag("column-id", "id", false), valueFlag("column-name", "text", false), valueFlag("lane-id", "id", false), valueFlag("lane-name", "text", false), valueFlag("type-id", "id", false), valueFlag("description", "text", false), valueFlag("size", "non-negative-int", false), valueFlag("due-date", "iso-8601-or-empty", false),
	}},
	{group: "cards", name: "archive", usage: "kaiten cards archive <card_id>", positionals: 1},
	{group: "cards", name: "unarchive", usage: "kaiten cards unarchive <card_id>", positionals: 1},
	{group: "cards", name: "delete", usage: "kaiten cards delete <card_id>", positionals: 1},
	{group: "comments", name: "list", usage: "kaiten comments list --card-id <id>", flags: []flagDef{valueFlag("card-id", "id", true)}},
	{group: "comments", name: "add", usage: "kaiten comments add --card-id <id> --text <text> [--type <1|2>]", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("text", "text", true), valueFlag("type", "1|2", false)}},
	{group: "blockers", name: "block", usage: "kaiten blockers block --card-id <id> [--reason <text>]", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("reason", "text", false)}},
	{group: "blockers", name: "unblock", usage: "kaiten blockers unblock --card-id <id> --blocker-id <id>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("blocker-id", "id", true)}},
	{group: "blockers", name: "delete", usage: "kaiten blockers delete --card-id <id> --blocker-id <id>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("blocker-id", "id", true)}},
	{group: "members", name: "list", usage: "kaiten members list --card-id <id>", flags: []flagDef{valueFlag("card-id", "id", true)}},
	{group: "tags", name: "list", usage: "kaiten tags list"},
	{group: "tags", name: "card-tags", usage: "kaiten tags card-tags --card-id <id>", flags: []flagDef{valueFlag("card-id", "id", true)}},
	{group: "tags", name: "add", usage: "kaiten tags add --card-id <id> --name <text>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("name", "text", true)}},
	{group: "tags", name: "remove", usage: "kaiten tags remove --card-id <id> --tag-id <id>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("tag-id", "id", true)}},
	{group: "checklists", name: "get", usage: "kaiten checklists get --card-id <id> --checklist-id <id>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("checklist-id", "id", true)}},
	{group: "checklists", name: "create", usage: "kaiten checklists create --card-id <id> [--name <text>]", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("name", "text", false)}},
	{group: "checklists", name: "delete", usage: "kaiten checklists delete --card-id <id> --checklist-id <id>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("checklist-id", "id", true)}},
	{group: "checklists", name: "add-item", usage: "kaiten checklists add-item --card-id <id> --checklist-id <id> --text <text>", flags: []flagDef{valueFlag("card-id", "id", true), valueFlag("checklist-id", "id", true), valueFlag("text", "text", true)}},
	{group: "checklists", name: "check", usage: "kaiten checklists check --card-id <id> --checklist-id <id> --item-id <id>", flags: checklistItemFlags()},
	{group: "checklists", name: "uncheck", usage: "kaiten checklists uncheck --card-id <id> --checklist-id <id> --item-id <id>", flags: checklistItemFlags()},
}

func valueFlag(name, value string, required bool) flagDef {
	return flagDef{name: name, value: value, required: required}
}
func boolFlag(name string) flagDef { return flagDef{name: name, boolean: true} }
func checklistItemFlags() []flagDef {
	return []flagDef{valueFlag("card-id", "id", true), valueFlag("checklist-id", "id", true), valueFlag("item-id", "id", true)}
}

// Run executes one CLI invocation and returns an exit status.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 {
		printTopHelp(stdout)
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(stdout, "kaiten %s\n", version.String())
		return 0
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printTopHelp(stdout)
		return 0
	}
	if args[0] == "completion" {
		return completion(args[1:], stdout, stderr)
	}
	if args[0] == "mcp" {
		return runMCPCommand(ctx, args[1:], stdin, stdout, stderr, dependencies, "kaiten mcp")
	}
	group := args[0]
	if !knownGroup(group) {
		return fail(stderr, "unknown command group %q", group)
	}
	if len(args) == 1 || args[1] == "--help" || args[1] == "-h" {
		printGroupHelp(stdout, group)
		return 0
	}
	spec, ok := findSpec(group, args[1])
	if !ok {
		return fail(stderr, "unknown %s command %q", group, args[1])
	}
	if containsHelp(args[2:]) {
		printCommandHelp(stdout, spec)
		return 0
	}
	parsed, err := parseCommand(spec, args[2:])
	if err != nil {
		return fail(stderr, "%v", err)
	}
	value, err := execute(ctx, spec, parsed)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "operation interrupted")
			return 130
		}
		return fail(stderr, "%v", err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fail(stderr, "write output: %v", err)
	}
	return 0
}

type parsedCommand struct {
	values      map[string]string
	present     map[string]bool
	positionals []string
}

func parseCommand(spec commandSpec, args []string) (parsedCommand, error) {
	defs := make(map[string]flagDef, len(spec.flags))
	for _, def := range spec.flags {
		defs[def.name] = def
	}
	parsed := parsedCommand{values: make(map[string]string), present: make(map[string]bool)}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			parsed.positionals = append(parsed.positionals, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			parsed.positionals = append(parsed.positionals, arg)
			continue
		}
		nameValue := strings.TrimPrefix(arg, "--")
		name, inline, hasInline := strings.Cut(nameValue, "=")
		def, ok := defs[name]
		if !ok {
			return parsed, fmt.Errorf("unknown flag --%s", name)
		}
		if parsed.present[name] {
			return parsed, fmt.Errorf("flag --%s may be supplied only once", name)
		}
		parsed.present[name] = true
		if def.boolean {
			if hasInline {
				if inline != "true" && inline != "false" {
					return parsed, fmt.Errorf("--%s accepts true or false", name)
				}
				parsed.values[name] = inline
			} else {
				parsed.values[name] = "true"
			}
			continue
		}
		if hasInline {
			parsed.values[name] = inline
			continue
		}
		if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
			return parsed, fmt.Errorf("--%s requires a value", name)
		}
		index++
		parsed.values[name] = args[index]
	}
	if len(parsed.positionals) != spec.positionals {
		return parsed, fmt.Errorf("%s expects %d positional argument(s)", spec.usage, spec.positionals)
	}
	for _, def := range spec.flags {
		if def.required && !parsed.present[def.name] {
			return parsed, fmt.Errorf("--%s is required", def.name)
		}
	}
	return parsed, nil
}

func execute(ctx context.Context, spec commandSpec, command parsedCommand) (any, error) {
	validated, err := validateLocal(spec, command)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(true, config.Overrides{})
	if err != nil {
		return nil, err
	}
	service := api.NewService(api.New(cfg))
	group, name := spec.group, spec.name

	switch group + "/" + name {
	case "spaces/list":
		value, _, err := service.ListSpaces(ctx)
		return value, err
	case "spaces/get":
		return service.GetSpace(ctx, validated.ids["positional-0"])
	case "boards/list":
		id := validated.ids["space-id"]
		value, _, err := service.ListBoards(ctx, &id, nil)
		return value, err
	case "boards/get":
		return service.GetBoard(ctx, validated.ids["positional-0"], false)
	case "columns/list", "lanes/list":
		field := "columns"
		if group == "lanes" {
			field = "lanes"
		}
		return service.GetPath(ctx, fmt.Sprintf("/boards/%d/%s", validated.ids["board-id"], field))
	case "cards/list":
		query := url.Values{"board_id": {strconv.FormatInt(validated.ids["board-id"], 10)}}
		if command.present["archived"] && command.values["archived"] != "false" {
			query.Set("condition", "2")
		} else if !command.present["all"] || command.values["all"] == "false" {
			query.Set("condition", "1")
		}
		return service.ListCards(ctx, query, 0, nil, false)
	case "cards/get":
		return service.GetCard(ctx, validated.ids["positional-0"], false, false, false)
	case "cards/create":
		return createCard(ctx, service, command, validated)
	case "cards/update":
		return updateCard(ctx, service, command, validated)
	case "cards/archive":
		return service.Mutate(ctx, http.MethodPatch, cardPath(validated.ids["positional-0"]), map[string]any{"condition": 2})
	case "cards/unarchive":
		return service.Mutate(ctx, http.MethodPatch, cardPath(validated.ids["positional-0"]), map[string]any{"condition": 1})
	case "cards/delete":
		id := validated.ids["positional-0"]
		if _, err := service.Mutate(ctx, http.MethodDelete, cardPath(id), nil); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": id}, nil
	case "comments/list":
		return service.GetPath(ctx, fmt.Sprintf("/cards/%d/comments", validated.ids["card-id"]))
	case "comments/add":
		commentType := int64(1)
		if command.present["type"] {
			commentType = validated.ids["type"]
		}
		return service.Mutate(ctx, http.MethodPost, fmt.Sprintf("/cards/%d/comments", validated.ids["card-id"]), map[string]any{"text": command.values["text"], "type": commentType})
	case "blockers/block":
		body := map[string]any{}
		if command.present["reason"] {
			body["reason"] = command.values["reason"]
		}
		return service.Mutate(ctx, http.MethodPost, fmt.Sprintf("/cards/%d/blockers", validated.ids["card-id"]), body)
	case "blockers/unblock", "blockers/delete":
		id := validated.ids["blocker-id"]
		if _, err := service.Mutate(ctx, http.MethodDelete, fmt.Sprintf("/cards/%d/blockers/%d", validated.ids["card-id"], id), nil); err != nil {
			return nil, err
		}
		key := "deleted"
		if name == "unblock" {
			key = "unblocked"
		}
		return map[string]any{key: id}, nil
	case "members/list":
		return service.GetPath(ctx, fmt.Sprintf("/cards/%d/members", validated.ids["card-id"]))
	case "tags/list":
		value, _, err := service.Discovery(ctx, "/tags", "tags")
		return value, err
	case "tags/card-tags":
		return service.GetPath(ctx, fmt.Sprintf("/cards/%d/tags", validated.ids["card-id"]))
	case "tags/add":
		return service.Mutate(ctx, http.MethodPost, fmt.Sprintf("/cards/%d/tags", validated.ids["card-id"]), map[string]any{"name": command.values["name"]})
	case "tags/remove":
		id := validated.ids["tag-id"]
		if _, err := service.Mutate(ctx, http.MethodDelete, fmt.Sprintf("/cards/%d/tags/%d", validated.ids["card-id"], id), nil); err != nil {
			return nil, err
		}
		return map[string]any{"removed_tag_id": id}, nil
	case "checklists/get":
		return service.GetPath(ctx, checklistPath(validated.ids["card-id"], validated.ids["checklist-id"]))
	case "checklists/create":
		checklistName := "Checklist"
		if command.present["name"] {
			checklistName = command.values["name"]
		}
		return service.Mutate(ctx, http.MethodPost, fmt.Sprintf("/cards/%d/checklists", validated.ids["card-id"]), map[string]any{"name": checklistName})
	case "checklists/delete":
		id := validated.ids["checklist-id"]
		if _, err := service.Mutate(ctx, http.MethodDelete, checklistPath(validated.ids["card-id"], id), nil); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": id}, nil
	case "checklists/add-item":
		path := fmt.Sprintf("%s/items", checklistPath(validated.ids["card-id"], validated.ids["checklist-id"]))
		return service.Mutate(ctx, http.MethodPost, path, map[string]any{"text": command.values["text"]})
	case "checklists/check", "checklists/uncheck":
		path := fmt.Sprintf("%s/items/%d", checklistPath(validated.ids["card-id"], validated.ids["checklist-id"]), validated.ids["item-id"])
		return service.Mutate(ctx, http.MethodPatch, path, map[string]any{"checked": name == "check"})
	default:
		return nil, errors.New("command is not implemented")
	}
}

type validation struct{ ids map[string]int64 }

func validateLocal(spec commandSpec, command parsedCommand) (validation, error) {
	result := validation{ids: make(map[string]int64)}
	for index, value := range command.positionals {
		id, err := positiveID(value)
		if err != nil {
			return result, fmt.Errorf("positional ID: %w", err)
		}
		result.ids[fmt.Sprintf("positional-%d", index)] = id
	}
	idFlags := map[string]bool{"space-id": true, "board-id": true, "column-id": true, "lane-id": true, "type-id": true, "card-id": true, "blocker-id": true, "tag-id": true, "checklist-id": true, "item-id": true}
	for name := range idFlags {
		if command.present[name] {
			id, err := positiveID(command.values[name])
			if err != nil {
				return result, fmt.Errorf("--%s: %w", name, err)
			}
			result.ids[name] = id
		}
	}
	for _, name := range []string{"title", "text", "name"} {
		if command.present[name] && strings.TrimSpace(command.values[name]) == "" {
			return result, fmt.Errorf("--%s must not be empty", name)
		}
	}
	if both(command, "archived", "all") && command.values["archived"] != "false" && command.values["all"] != "false" {
		return result, errors.New("--archived and --all are mutually exclusive")
	}
	if both(command, "column-id", "column-name") || both(command, "lane-id", "lane-name") {
		return result, errors.New("ID and name selectors are mutually exclusive")
	}
	if spec.group == "cards" && spec.name == "create" {
		if command.present["column-id"] == command.present["column-name"] {
			return result, errors.New("exactly one of --column-id or --column-name is required")
		}
		if command.present["due-date"] && command.values["due-date"] == "" {
			return result, errors.New("--due-date must not be empty when creating a card")
		}
	}
	if spec.group == "blockers" && spec.name == "block" {
		reason := strings.TrimSpace(command.values["reason"])
		if !command.present["reason"] || reason == "" {
			return result, errors.New("--reason is required by the Kaiten block-card API and must not be empty")
		}
		if len([]rune(command.values["reason"])) > 4096 {
			return result, errors.New("--reason must not exceed 4096 characters")
		}
	}
	if spec.group == "cards" && spec.name == "update" {
		updateCount := 0
		for _, name := range []string{"board-id", "title", "column-id", "column-name", "lane-id", "lane-name", "type-id", "description", "size", "due-date"} {
			if command.present[name] {
				updateCount++
			}
		}
		if updateCount == 0 {
			return result, errors.New("at least one update field is required")
		}
		if (command.present["column-name"] || command.present["lane-name"]) && !command.present["board-id"] {
			return result, errors.New("--board-id is required with a column or lane name")
		}
	}
	if command.present["column-name"] && strings.TrimSpace(command.values["column-name"]) == "" {
		return result, errors.New("--column-name must not be empty")
	}
	if command.present["lane-name"] && strings.TrimSpace(command.values["lane-name"]) == "" {
		return result, errors.New("--lane-name must not be empty")
	}
	if command.present["position"] {
		value, err := strconv.ParseInt(command.values["position"], 10, 64)
		if err != nil || (value != 1 && value != 2) {
			return result, errors.New("--position must be 1 or 2")
		}
		result.ids["position"] = value
	}
	if command.present["type"] {
		value, err := strconv.ParseInt(command.values["type"], 10, 64)
		if err != nil || (value != 1 && value != 2) {
			return result, errors.New("--type must be 1 or 2")
		}
		result.ids["type"] = value
	}
	if command.present["size"] {
		value, err := strconv.ParseInt(command.values["size"], 10, 64)
		minimum := int64(0)
		if spec.name == "create" {
			minimum = 1
		}
		if err != nil || value < minimum {
			return result, fmt.Errorf("--size must be an integer of at least %d", minimum)
		}
		result.ids["size"] = value
	}
	if command.present["due-date"] && command.values["due-date"] != "" {
		if _, err := time.Parse(time.RFC3339, command.values["due-date"]); err != nil {
			return result, errors.New("--due-date must be an ISO 8601 date-time or empty")
		}
	}
	if command.present["name"] && spec.group == "checklists" && strings.TrimSpace(command.values["name"]) == "" {
		return result, errors.New("--name must not be empty when supplied")
	}
	return result, nil
}

func createCard(ctx context.Context, service *api.Service, command parsedCommand, validated validation) (any, error) {
	body := map[string]any{"title": command.values["title"], "board_id": validated.ids["board-id"]}
	if command.present["column-id"] {
		body["column_id"] = validated.ids["column-id"]
	} else {
		column, err := service.ResolveColumn(ctx, validated.ids["board-id"], command.values["column-name"])
		if err != nil {
			return nil, err
		}
		body["column_id"] = column.ID
	}
	if command.present["lane-id"] {
		body["lane_id"] = validated.ids["lane-id"]
	} else if command.present["lane-name"] {
		lane, err := service.ResolveLane(ctx, validated.ids["board-id"], command.values["lane-name"])
		if err != nil {
			return nil, err
		}
		body["lane_id"] = lane.ID
	}
	copyCreateUpdateFields(body, command, validated)
	return service.Mutate(ctx, http.MethodPost, "/cards", body)
}

func updateCard(ctx context.Context, service *api.Service, command parsedCommand, validated validation) (any, error) {
	body := map[string]any{}
	if command.present["board-id"] {
		body["board_id"] = validated.ids["board-id"]
	}
	if command.present["column-id"] {
		body["column_id"] = validated.ids["column-id"]
	} else if command.present["column-name"] {
		column, err := service.ResolveColumn(ctx, validated.ids["board-id"], command.values["column-name"])
		if err != nil {
			return nil, err
		}
		body["column_id"] = column.ID
	}
	if command.present["lane-id"] {
		body["lane_id"] = validated.ids["lane-id"]
	} else if command.present["lane-name"] {
		lane, err := service.ResolveLane(ctx, validated.ids["board-id"], command.values["lane-name"])
		if err != nil {
			return nil, err
		}
		body["lane_id"] = lane.ID
	}
	copyCreateUpdateFields(body, command, validated)
	return service.Mutate(ctx, http.MethodPatch, cardPath(validated.ids["positional-0"]), body)
}

func copyCreateUpdateFields(body map[string]any, command parsedCommand, validated validation) {
	for _, name := range []string{"title", "description", "due-date"} {
		if command.present[name] {
			key := strings.ReplaceAll(name, "-", "_")
			value := any(command.values[name])
			if (name == "due-date" || name == "description") && command.values[name] == "" {
				value = nil
			}
			body[key] = value
		}
	}
	if command.present["type-id"] {
		body["type_id"] = validated.ids["type-id"]
	}
	if command.present["position"] {
		body["position"] = validated.ids["position"]
	}
	if command.present["size"] {
		body["size_text"] = strconv.FormatInt(validated.ids["size"], 10)
	}
}

func positiveID(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("must be a positive decimal integer")
	}
	return value, nil
}

func objectArrayField(value any, field string) ([]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Kaiten returned an invalid board object")
	}
	items, ok := object[field].([]any)
	if !ok {
		return []any{}, nil
	}
	return items, nil
}

func both(command parsedCommand, first, second string) bool {
	return command.present[first] && command.present[second]
}
func cardPath(id int64) string { return fmt.Sprintf("/cards/%d", id) }
func checklistPath(cardID, checklistID int64) string {
	return fmt.Sprintf("/cards/%d/checklists/%d", cardID, checklistID)
}

// RunStandaloneMCP executes the kaiten-mcp entrypoint with injectable
// lifecycle dependencies. Command help is handled before configuration or any
// lifecycle callback is reached.
func RunStandaloneMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies Dependencies) int {
	return runMCPCommand(ctx, args, stdin, stdout, stderr, dependencies, "kaiten-mcp")
}

func runMCPCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies Dependencies, usagePrefix string) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			if len(args) == 2 && isHelp(args[1]) {
				printMCPCommandHelp(stdout, usagePrefix, "version")
				return 0
			}
			if len(args) != 1 {
				return fail(stderr, "%s version accepts no arguments", usagePrefix)
			}
			fmt.Fprintf(stdout, "kaiten-mcp %s\n", version.String())
			return 0
		case "install":
			if len(args) == 2 && isHelp(args[1]) {
				printMCPCommandHelp(stdout, usagePrefix, "install")
				return 0
			}
			if len(args) != 1 {
				return fail(stderr, "%s install accepts no arguments", usagePrefix)
			}
			if dependencies.MCPInstall == nil {
				return fail(stderr, "installer is unavailable")
			}
			return dependencies.MCPInstall(ctx, stdin, stdout, stderr)
		case "uninstall":
			if len(args) == 2 && isHelp(args[1]) {
				printMCPCommandHelp(stdout, usagePrefix, "uninstall")
				return 0
			}
			if len(args) != 1 {
				return fail(stderr, "%s uninstall accepts no arguments", usagePrefix)
			}
			if dependencies.MCPUninstall == nil {
				return fail(stderr, "uninstaller is unavailable")
			}
			return dependencies.MCPUninstall(ctx, stdin, stdout, stderr)
		case "--help", "-h":
			printMCPHelp(stdout, usagePrefix)
			return 0
		}
	}
	if dependencies.MCPRun == nil {
		return fail(stderr, "MCP server is unavailable")
	}
	return dependencies.MCPRun(ctx, args, stdin, stdout, stderr)
}

func completion(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return fail(stderr, "completion requires one shell: bash, fish, powershell, or zsh")
	}
	groups := "spaces boards columns lanes cards comments blockers members tags checklists mcp"
	switch args[0] {
	case "bash":
		fmt.Fprintf(stdout, "complete -W %q kaiten\n", groups)
	case "zsh":
		fmt.Fprintf(stdout, "#compdef kaiten\n_arguments '1:command:(%s)'\n", groups)
	case "fish":
		for _, group := range strings.Fields(groups) {
			fmt.Fprintf(stdout, "complete -c kaiten -f -a %s\n", group)
		}
	case "powershell":
		fmt.Fprintf(stdout, "Register-ArgumentCompleter -Native -CommandName kaiten -ScriptBlock { param($wordToComplete) %q -split ' ' | Where-Object { $_ -like \"$wordToComplete*\" } }\n", groups)
	default:
		return fail(stderr, "unsupported shell %q", args[0])
	}
	return 0
}

func printTopHelp(writer io.Writer) {
	fmt.Fprintln(writer, `Usage: kaiten <group> <command> [flags]

Groups:
  spaces      list and inspect spaces
  boards      list and inspect boards
  columns     list board columns
  lanes       list board lanes
  cards       list, inspect, create, update, archive, unarchive, or delete cards
  comments    list or add comments
  blockers    block, unblock, or remove blockers
  members     list card members
  tags        list and manage tags
  checklists  inspect and manage checklists
  mcp         run, install, uninstall, or inspect the MCP service

Other commands:
  completion <bash|fish|powershell|zsh>
  --version`)
}

func printGroupHelp(writer io.Writer, group string) {
	fmt.Fprintf(writer, "Usage: kaiten %s <command> [flags]\n\nCommands:\n", group)
	for _, spec := range commandSpecs {
		if spec.group == group {
			fmt.Fprintf(writer, "  %-12s %s\n", spec.name, spec.usage)
		}
	}
}

func printCommandHelp(writer io.Writer, spec commandSpec) {
	fmt.Fprintf(writer, "Usage: %s\n", spec.usage)
	if len(spec.flags) > 0 {
		fmt.Fprintln(writer, "\nFlags:")
	}
	for _, flag := range spec.flags {
		suffix := ""
		if !flag.boolean {
			suffix = " <" + flag.value + ">"
		}
		required := ""
		if flag.required {
			required = " (required)"
		}
		fmt.Fprintf(writer, "  --%s%s%s\n", flag.name, suffix, required)
	}
}

func printMCPHelp(writer io.Writer, usagePrefix string) {
	fmt.Fprintf(writer, `Usage: %s [--transport <stdio|streamable-http>] [--host <bind-host>] [--port <1..65535>] [--streamable-http-path <path>]
       %s install
       %s uninstall
       %s version
`, usagePrefix, usagePrefix, usagePrefix, usagePrefix)
}

func printMCPCommandHelp(writer io.Writer, usagePrefix, command string) {
	fmt.Fprintf(writer, "Usage: %s %s\n", usagePrefix, command)
}

func knownGroup(group string) bool {
	for _, spec := range commandSpecs {
		if spec.group == group {
			return true
		}
	}
	return false
}

func findSpec(group, name string) (commandSpec, bool) {
	for _, spec := range commandSpecs {
		if spec.group == group && spec.name == name {
			return spec, true
		}
	}
	return commandSpec{}, false
}

func containsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func isHelp(arg string) bool {
	return arg == "--help" || arg == "-h"
}

func fail(stderr io.Writer, format string, values ...any) int {
	fmt.Fprintf(stderr, "error: "+format+"\n", values...)
	return 1
}
