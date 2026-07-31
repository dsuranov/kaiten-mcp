package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/api"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content           []toolContent  `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

func (s *Server) callTool(ctx context.Context, spec toolSpec, arguments map[string]any) toolResult {
	if err := validateDomainInput(spec, arguments); err != nil {
		return failureResult(err)
	}
	data, cached, err := s.executeTool(ctx, spec.name, arguments)
	if err != nil {
		return failureResult(err)
	}
	return successResult(data, cached)
}

func (s *Server) executeTool(ctx context.Context, name string, arguments map[string]any) (any, bool, error) {
	switch name {
	case "get_board":
		data, err := s.service.GetBoard(ctx, integerArgument(arguments, "board_id"), boolArgument(arguments, "include_cards", false))
		return data, false, err
	case "get_board_structure":
		return s.service.BoardStructure(ctx, stringArgument(arguments, "board"))
	case "get_card":
		return s.getCard(ctx, arguments)
	case "get_card_checklists":
		card, err := s.service.GetCard(ctx, integerArgument(arguments, "card_id"), false, false, false)
		if err != nil {
			return nil, false, err
		}
		return arrayField(card, "checklists"), false, nil
	case "get_card_children":
		data, err := s.service.GetPath(ctx, fmt.Sprintf("/cards/%d/children", integerArgument(arguments, "card_id")))
		return data, false, err
	case "get_current_user":
		data, err := s.service.CurrentUser(ctx)
		return data, false, err
	case "get_member_cards":
		user, _, err := s.service.ResolveUser(ctx, stringArgument(arguments, "user"))
		if err != nil {
			return nil, false, err
		}
		query := url.Values{"member_ids": {strconv.FormatInt(user.ID, 10)}}
		data, err := s.cardsFromArguments(ctx, query, arguments)
		return data, false, err
	case "get_my_cards":
		current, err := s.service.CurrentUser(ctx)
		if err != nil {
			return nil, false, err
		}
		id := objectInteger(current, "id")
		if id <= 0 {
			return nil, false, domainError("upstream", "Kaiten returned a current user without a valid ID")
		}
		data, err := s.cardsFromArguments(ctx, url.Values{"owner_id": {strconv.FormatInt(id, 10)}}, arguments)
		return data, false, err
	case "get_responsible_cards":
		user, _, err := s.service.ResolveUser(ctx, stringArgument(arguments, "user"))
		if err != nil {
			return nil, false, err
		}
		data, err := s.cardsFromArguments(ctx, url.Values{"responsible_id": {strconv.FormatInt(user.ID, 10)}}, arguments)
		return data, false, err
	case "get_server_info":
		return map[string]any{"version": version.String(), "runtime": runtime.Version()}, false, nil
	case "get_space":
		data, err := s.service.GetSpace(ctx, integerArgument(arguments, "space_id"))
		return data, false, err
	case "list_boards":
		limit := optionalInteger(arguments, "limit")
		if limit != nil && *limit == 0 {
			return []any{}, false, nil
		}
		space := optionalInt64(arguments, "space_id")
		data, cached, err := s.service.ListBoards(ctx, space, limit)
		return data, cached, err
	case "list_card_types":
		return s.discovery(ctx, "/card-types", "card-types")
	case "list_custom_properties":
		return s.discovery(ctx, "/company/custom-properties", "custom-properties")
	case "list_spaces":
		return s.service.ListSpaces(ctx)
	case "list_tags":
		return s.discovery(ctx, "/tags", "tags")
	case "list_users":
		limit := optionalInteger(arguments, "limit")
		if limit != nil && *limit == 0 {
			return []any{}, false, nil
		}
		data, cached, err := s.service.ListUsers(ctx, optionalInt64(arguments, "space_id"), optionalInteger(arguments, "skip"), limit)
		return data, cached, err
	case "search_cards":
		query := url.Values{}
		for _, name := range []string{"query", "board_id", "space_id"} {
			if value, present := arguments[name]; present && value != nil {
				query.Set(name, scalarString(value))
			}
		}
		if !boolArgument(arguments, "include_archived", false) {
			query.Set("condition", "1")
		}
		data, err := s.cardsFromArguments(ctx, query, arguments)
		return data, false, err
	case "add_checklist_item":
		path := fmt.Sprintf("/cards/%d/checklists/%d/items", integerArgument(arguments, "card_id"), integerArgument(arguments, "checklist_id"))
		return s.mutate(ctx, http.MethodPost, path, map[string]any{"text": stringArgument(arguments, "text")})
	case "add_comment":
		path := fmt.Sprintf("/cards/%d/comments", integerArgument(arguments, "card_id"))
		return s.mutate(ctx, http.MethodPost, path, map[string]any{"text": stringArgument(arguments, "text")})
	case "add_external_link":
		body := map[string]any{"url": stringArgument(arguments, "url")}
		copyPresent(body, arguments, "description")
		path := fmt.Sprintf("/cards/%d/external-links", integerArgument(arguments, "card_id"))
		return s.mutate(ctx, http.MethodPost, path, body)
	case "add_watcher":
		return s.addMember(ctx, arguments)
	case "create_card":
		return s.createCard(ctx, arguments)
	case "create_checklist":
		path := fmt.Sprintf("/cards/%d/checklists", integerArgument(arguments, "card_id"))
		return s.mutate(ctx, http.MethodPost, path, map[string]any{"name": stringArgument(arguments, "name")})
	case "delete_checklist":
		cardID, checklistID := integerArgument(arguments, "card_id"), integerArgument(arguments, "checklist_id")
		if _, err := s.service.Mutate(ctx, http.MethodDelete, fmt.Sprintf("/cards/%d/checklists/%d", cardID, checklistID), nil); err != nil {
			return nil, false, err
		}
		return map[string]any{"deleted": checklistID}, false, nil
	case "delete_checklist_item":
		cardID, checklistID, itemID := integerArgument(arguments, "card_id"), integerArgument(arguments, "checklist_id"), integerArgument(arguments, "item_id")
		path := fmt.Sprintf("/cards/%d/checklists/%d/items/%d", cardID, checklistID, itemID)
		if _, err := s.service.Mutate(ctx, http.MethodDelete, path, nil); err != nil {
			return nil, false, err
		}
		return map[string]any{"deleted": itemID}, false, nil
	case "link_child_card":
		parent, child := integerArgument(arguments, "parent_card_id"), integerArgument(arguments, "child_card_id")
		return s.mutate(ctx, http.MethodPost, fmt.Sprintf("/cards/%d/children", parent), map[string]any{"card_id": child})
	case "move_card":
		return s.moveCard(ctx, arguments)
	case "remove_member":
		return s.removeMember(ctx, arguments)
	case "set_responsible":
		return s.setResponsible(ctx, arguments)
	case "unlink_child_card":
		parent, child := integerArgument(arguments, "parent_card_id"), integerArgument(arguments, "child_card_id")
		if _, err := s.service.Mutate(ctx, http.MethodDelete, fmt.Sprintf("/cards/%d/children/%d", parent, child), nil); err != nil {
			return nil, false, err
		}
		return map[string]any{"unlinked_child_card_id": child}, false, nil
	case "update_card":
		return s.updateCard(ctx, arguments)
	case "update_checklist_item":
		return s.updateChecklistItem(ctx, arguments)
	default:
		return nil, false, domainError("validation", "unknown tool")
	}
}

func (s *Server) getCard(ctx context.Context, arguments map[string]any) (any, bool, error) {
	id := integerArgument(arguments, "card_id")
	card, err := s.service.GetCard(ctx, id, false, false, false)
	if err != nil {
		return nil, false, err
	}
	object, ok := card.(map[string]any)
	if !ok {
		return nil, false, domainError("upstream", "Kaiten returned an invalid card object")
	}
	if boolArgument(arguments, "include_comments", false) {
		comments, err := s.service.GetPath(ctx, fmt.Sprintf("/cards/%d/comments", id))
		if err != nil {
			return nil, false, err
		}
		object["comments"] = comments
	} else {
		delete(object, "comments")
	}
	if boolArgument(arguments, "include_members", true) {
		members, err := s.service.GetPath(ctx, fmt.Sprintf("/cards/%d/members", id))
		if err != nil {
			return nil, false, err
		}
		object["members"] = members
	} else {
		delete(object, "members")
	}
	if boolArgument(arguments, "include_relations", true) {
		children, err := s.service.GetPath(ctx, fmt.Sprintf("/cards/%d/children", id))
		if err != nil {
			return nil, false, err
		}
		object["children"] = children
	} else {
		for _, field := range []string{"children", "parents", "children_ids", "parents_ids"} {
			delete(object, field)
		}
	}
	return object, false, nil
}

func (s *Server) cardsFromArguments(ctx context.Context, query url.Values, arguments map[string]any) (any, error) {
	skip := 0
	if value := optionalInteger(arguments, "skip"); value != nil {
		skip = *value
	}
	return s.service.ListCards(ctx, query, skip, optionalInteger(arguments, "limit"), boolArgument(arguments, "fetch_all", false))
}

func (s *Server) discovery(ctx context.Context, path, key string) (any, bool, error) {
	return s.service.Discovery(ctx, path, key)
}

func (s *Server) mutate(ctx context.Context, method, path string, body any) (any, bool, error) {
	data, err := s.service.Mutate(ctx, method, path, body)
	return data, false, err
}

func (s *Server) addMember(ctx context.Context, arguments map[string]any) (any, bool, error) {
	user, _, err := s.service.ResolveUser(ctx, stringArgument(arguments, "user"))
	if err != nil {
		return nil, false, err
	}
	data, err := s.service.Mutate(ctx, http.MethodPost, fmt.Sprintf("/cards/%d/members", integerArgument(arguments, "card_id")), map[string]any{"user_id": user.ID})
	return data, false, err
}

func (s *Server) createCard(ctx context.Context, arguments map[string]any) (any, bool, error) {
	board, _, err := s.service.ResolveBoard(ctx, stringArgument(arguments, "board"))
	if err != nil {
		return nil, false, err
	}
	column, err := s.service.ResolveColumn(ctx, board.ID, stringArgument(arguments, "column"))
	if err != nil {
		return nil, false, err
	}
	body := map[string]any{"title": stringArgument(arguments, "title"), "board_id": board.ID, "column_id": column.ID}
	for _, name := range []string{"lane_id", "type_id", "description", "due_date", "size_text", "planned_start", "planned_end", "tag_ids"} {
		copyPresent(body, arguments, name)
	}
	if owner, present := arguments["owner"]; present && owner != nil {
		user, _, err := s.service.ResolveUser(ctx, owner.(string))
		if err != nil {
			return nil, false, err
		}
		body["owner_id"] = user.ID
	}
	if properties, present := arguments["properties"]; present && properties != nil {
		resolved, _, err := s.resolveProperties(ctx, properties.(map[string]any))
		if err != nil {
			return nil, false, err
		}
		body["properties"] = resolved
	}
	data, err := s.service.Mutate(ctx, http.MethodPost, "/cards", body)
	return data, false, err
}

func (s *Server) moveCard(ctx context.Context, arguments map[string]any) (any, bool, error) {
	cardID := integerArgument(arguments, "card_id")
	var boardID int64
	if selector, present := arguments["board"]; present && selector != nil {
		board, _, err := s.service.ResolveBoard(ctx, selector.(string))
		if err != nil {
			return nil, false, err
		}
		boardID = board.ID
	} else {
		card, err := s.service.GetCard(ctx, cardID, false, false, false)
		if err != nil {
			return nil, false, err
		}
		boardID = objectInteger(card, "board_id")
		if boardID <= 0 {
			return nil, false, domainError("upstream", "Kaiten card has no valid board ID")
		}
	}
	column, err := s.service.ResolveColumn(ctx, boardID, stringArgument(arguments, "column"))
	if err != nil {
		return nil, false, err
	}
	body := map[string]any{"board_id": boardID, "column_id": column.ID}
	copyPresent(body, arguments, "lane_id")
	data, err := s.service.Mutate(ctx, http.MethodPatch, fmt.Sprintf("/cards/%d", cardID), body)
	return data, false, err
}

func (s *Server) removeMember(ctx context.Context, arguments map[string]any) (any, bool, error) {
	user, _, err := s.service.ResolveUser(ctx, stringArgument(arguments, "user"))
	if err != nil {
		return nil, false, err
	}
	cardID := integerArgument(arguments, "card_id")
	if _, err := s.service.Mutate(ctx, http.MethodDelete, fmt.Sprintf("/cards/%d/members/%d", cardID, user.ID), nil); err != nil {
		return nil, false, err
	}
	return map[string]any{"removed_user_id": user.ID}, false, nil
}

func (s *Server) setResponsible(ctx context.Context, arguments map[string]any) (any, bool, error) {
	user, _, err := s.service.ResolveUser(ctx, stringArgument(arguments, "user"))
	if err != nil {
		return nil, false, err
	}
	path := fmt.Sprintf("/cards/%d/members/%d", integerArgument(arguments, "card_id"), user.ID)
	data, err := s.service.Mutate(ctx, http.MethodPatch, path, map[string]any{"type": 2})
	return data, false, err
}

func (s *Server) updateCard(ctx context.Context, arguments map[string]any) (any, bool, error) {
	body := map[string]any{}
	for _, name := range []string{"title", "description", "due_date", "type_id", "size_text", "planned_start", "planned_end", "tag_ids"} {
		copyPresent(body, arguments, name)
	}
	if owner, present := arguments["owner"]; present {
		if owner == nil {
			body["owner_id"] = nil
		} else {
			user, _, err := s.service.ResolveUser(ctx, owner.(string))
			if err != nil {
				return nil, false, err
			}
			body["owner_id"] = user.ID
		}
	}
	if properties, present := arguments["properties"]; present {
		if properties == nil {
			body["properties"] = nil
		} else {
			resolved, _, err := s.resolveProperties(ctx, properties.(map[string]any))
			if err != nil {
				return nil, false, err
			}
			body["properties"] = resolved
		}
	}
	if len(body) == 0 {
		return nil, false, domainError("validation", "update_card requires at least one field to change")
	}
	data, err := s.service.Mutate(ctx, http.MethodPatch, fmt.Sprintf("/cards/%d", integerArgument(arguments, "card_id")), body)
	return data, false, err
}

func (s *Server) updateChecklistItem(ctx context.Context, arguments map[string]any) (any, bool, error) {
	body := map[string]any{}
	for _, name := range []string{"text", "checked"} {
		if value, present := arguments[name]; present && value != nil {
			body[name] = value
		}
	}
	if len(body) == 0 {
		return nil, false, domainError("validation", "update_checklist_item requires text or checked")
	}
	path := fmt.Sprintf("/cards/%d/checklists/%d/items/%d", integerArgument(arguments, "card_id"), integerArgument(arguments, "checklist_id"), integerArgument(arguments, "item_id"))
	return s.mutate(ctx, http.MethodPatch, path, body)
}

func (s *Server) resolveProperties(ctx context.Context, requested map[string]any) (map[string]any, bool, error) {
	value, cached, err := s.service.Discovery(ctx, "/company/custom-properties", "custom-properties")
	if err != nil {
		return nil, false, err
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false, domainError("upstream", "Kaiten returned an invalid custom-property list")
	}
	resources := make([]api.Resource, 0, len(items))
	byID := make(map[int64]map[string]any)
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := objectInteger(object, "id")
		name, _ := object["name"].(string)
		if id > 0 {
			resources = append(resources, api.Resource{ID: id, Name: name})
			byID[id] = object
		}
	}
	resolved := make(map[string]any, len(requested))
	for propertyName, rawValue := range requested {
		property, err := api.Resolve(propertyName, resources, false)
		if err != nil {
			return nil, false, err
		}
		definition := byID[property.ID]
		kind, _ := definition["type"].(string)
		valueText := rawValue.(string)
		var output any = valueText
		switch strings.ToLower(kind) {
		case "number", "numeric":
			number, err := strconv.ParseFloat(strings.TrimSpace(valueText), 64)
			if err != nil {
				return nil, false, domainError("validation", fmt.Sprintf("custom property %q requires a number", propertyName))
			}
			output = number
		case "select", "single_select", "multi_select":
			options := propertyOptions(definition)
			if len(options) == 0 {
				optionValue, optionCached, err := s.service.Discovery(ctx, fmt.Sprintf("/company/custom-properties/%d/select-values", property.ID), fmt.Sprintf("custom-property:%d:select-values", property.ID))
				if err != nil {
					return nil, false, err
				}
				cached = cached && optionCached
				options = optionResources(optionValue)
			}
			parts := []string{valueText}
			multi, _ := definition["multi_select"].(bool)
			if strings.EqualFold(kind, "multi_select") || multi {
				parts = strings.Split(valueText, ",")
			}
			ids := make([]int64, 0, len(parts))
			for _, part := range parts {
				option, err := api.Resolve(strings.TrimSpace(part), options, false)
				if err != nil {
					return nil, false, err
				}
				ids = append(ids, option.ID)
			}
			if len(parts) == 1 && !multi && !strings.EqualFold(kind, "multi_select") {
				output = ids[0]
			} else {
				output = ids
			}
		}
		resolved[fmt.Sprintf("id_%d", property.ID)] = output
	}
	return resolved, cached, nil
}

func propertyOptions(definition map[string]any) []api.Resource {
	for _, key := range []string{"values", "select_values", "options"} {
		if _, ok := definition[key].([]any); ok {
			return optionResources(definition[key])
		}
	}
	return nil
}

func optionResources(value any) []api.Resource {
	items, _ := value.([]any)
	resources := make([]api.Resource, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := object["name"].(string)
		if name == "" {
			name, _ = object["value"].(string)
		}
		resources = append(resources, api.Resource{ID: objectInteger(object, "id"), Name: name})
	}
	return resources
}

func validateDomainInput(spec toolSpec, arguments map[string]any) error {
	for name, value := range arguments {
		if value == nil {
			continue
		}
		if strings.HasSuffix(name, "_id") {
			if number, ok := value.(float64); ok && number <= 0 {
				return domainError("validation", fmt.Sprintf("%s must be positive", name))
			}
		}
		if text, ok := value.(string); ok {
			if (requiredName(spec.required, name) || name == "title") && strings.TrimSpace(text) == "" {
				return domainError("validation", fmt.Sprintf("%s must not be empty", name))
			}
			if name == "due_date" || name == "planned_start" || name == "planned_end" {
				if _, err := time.Parse(time.RFC3339, text); err != nil {
					return domainError("validation", fmt.Sprintf("%s must be an ISO 8601 date-time", name))
				}
			}
			if name == "url" {
				parsed, err := url.Parse(text)
				if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
					return domainError("validation", "url must be an absolute http or https URL without credentials")
				}
			}
		}
	}
	if title, present := arguments["title"]; present && title == nil {
		return domainError("validation", "title cannot be null")
	}
	return nil
}

func requiredName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func successResult(data any, cached bool) toolResult {
	envelope := map[string]any{"ok": true, "data": data, "meta": map[string]any{"source": "kaiten", "cached": cached}}
	return envelopeResult(envelope)
}

func failureResult(err error) toolResult {
	kind, message := "upstream", "Kaiten operation failed"
	var status *int
	var apiError *api.Error
	if errors.As(err, &apiError) {
		kind, message, status = apiError.Type, apiError.Message, apiError.StatusCode
	}
	errorObject := map[string]any{"type": kind, "message": message}
	if status != nil {
		errorObject["status_code"] = *status
	}
	return envelopeResult(map[string]any{"ok": false, "error": errorObject})
}

func envelopeResult(envelope map[string]any) toolResult {
	encoded, _ := json.MarshalIndent(envelope, "", "  ")
	return toolResult{Content: []toolContent{{Type: "text", Text: string(encoded)}}, StructuredContent: envelope, IsError: false}
}

func domainError(kind, message string) error { return &api.Error{Type: kind, Message: message} }

func integerArgument(arguments map[string]any, name string) int64 {
	return int64(arguments[name].(float64))
}
func stringArgument(arguments map[string]any, name string) string { return arguments[name].(string) }

func boolArgument(arguments map[string]any, name string, fallback bool) bool {
	value, present := arguments[name]
	if !present || value == nil {
		return fallback
	}
	return value.(bool)
}

func optionalInteger(arguments map[string]any, name string) *int {
	value, present := arguments[name]
	if !present || value == nil {
		return nil
	}
	converted := int(value.(float64))
	return &converted
}

func optionalInt64(arguments map[string]any, name string) *int64 {
	value, present := arguments[name]
	if !present || value == nil {
		return nil
	}
	converted := int64(value.(float64))
	return &converted
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return fmt.Sprint(typed)
	}
}

func copyPresent(destination, source map[string]any, name string) {
	if value, present := source[name]; present {
		destination[name] = value
	}
}

func arrayField(value any, name string) []any {
	object, ok := value.(map[string]any)
	if !ok {
		return []any{}
	}
	items, ok := object[name].([]any)
	if !ok {
		return []any{}
	}
	return items
}

func objectInteger(value any, name string) int64 {
	object, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	switch typed := object[name].(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}
