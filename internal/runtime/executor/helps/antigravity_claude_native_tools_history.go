package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// nativeHistoryAlias binds a client-visible call to its recorded upstream identity.
type nativeHistoryAlias struct {
	clientName string
	originalID string
	nativeID   string
	nativeName string
	nativeArgs json.RawMessage
}

// RestoreClaudeNativeToolHistory bridges mapped client IDs back to native opaque
// IDs before replay validates context and decides whether signatures may be used.
// Only recorded one-to-one mappings are eligible; names alone prove no identity.
func RestoreClaudeNativeToolHistory(ctx context.Context, payload []byte, items [][]byte) ([]byte, error) {
	state := claudeNativeToolsFromCtx(ctx)
	if state == nil {
		return payload, nil
	}
	aliases := make(map[string]nativeHistoryAlias)
	ambiguous := make(map[string]bool)
	for _, item := range items {
		record := gjson.ParseBytes(item)
		if record.Get("type").String() != "function_call_part" {
			continue
		}
		name, id := record.Get("name").String(), record.Get("call_id").String()
		args := record.Get("args")
		if args.Type == gjson.String {
			args = gjson.Parse(args.String())
		}
		mapper := agyClaudeFunctionMappers[name]
		if mapper == nil || id == "" || !args.IsObject() {
			continue
		}
		// Reason: Current tools need not match the shell available on a historical
		// turn. Both variants remain constrained by the client ID and argument hash.
		seen := make(map[string]bool)
		for _, powerShell := range []bool{false, true} {
			calls := mapper(args, &claudeNativeToolsState{preferPowerShell: powerShell})
			if len(calls) != 1 {
				continue
			}
			clientArgs, err := marshalArgsNoHTMLEscape(calls[0].args)
			if err != nil {
				return nil, fmt.Errorf("encode native tool history alias: %w", err)
			}
			clientID := util.GeminiClaudeToolUseID(id, calls[0].name, string(clientArgs))
			if seen[clientID] {
				continue
			}
			seen[clientID] = true
			// Reason: Lossy mappings can collapse distinct native calls with a reused
			// provider ID. Never select a ledger record by insertion order.
			if _, exists := aliases[clientID]; exists {
				ambiguous[clientID] = true
			}
			aliases[clientID] = nativeHistoryAlias{
				clientName: calls[0].name,
				originalID: id,
				nativeID:   util.GeminiClaudeToolUseID(id, name, args.Raw),
				nativeName: name,
				nativeArgs: json.RawMessage(args.Raw),
			}
		}
	}
	for id := range ambiguous {
		delete(aliases, id)
	}
	if len(aliases) == 0 {
		return payload, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("decode native tool history: %w", err)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(doc["request"], &request); err != nil {
		return nil, fmt.Errorf("decode native tool history request: %w", err)
	}
	var contents []map[string]json.RawMessage
	if err := json.Unmarshal(request["contents"], &contents); err != nil {
		return nil, fmt.Errorf("decode native tool history contents: %w", err)
	}
	// Reason: Validate client call arguments before restoring its matching result;
	// an opaque ID copied onto an edited call is not sufficient provenance.
	eligible := make(map[string]nativeHistoryAlias)
	callCounts, resultCounts := make(map[string]int), make(map[string]int)
	matchingResults := make(map[string]int)
	gjson.GetBytes(payload, "request.contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			call := part.Get("functionCall")
			id := call.Get("id").String()
			if call.Exists() {
				callCounts[id]++
			}
			response := part.Get("functionResponse")
			responseID := response.Get("id").String()
			if response.Exists() {
				resultCounts[responseID]++
			}
			if alias, ok := aliases[responseID]; ok && response.Get("name").String() == alias.clientName {
				matchingResults[responseID]++
			}
			alias, ok := aliases[id]
			if ok && call.Get("name").String() == alias.clientName &&
				util.GeminiClaudeToolUseID(alias.originalID, alias.clientName, call.Get("args").Raw) == id {
				eligible[id] = alias
			}
			return true
		})
		return true
	})
	for id := range eligible {
		if callCounts[id] != 1 || resultCounts[id] != 1 || matchingResults[id] != 1 {
			delete(eligible, id)
		}
	}
	if len(eligible) == 0 {
		return payload, nil
	}
	for _, content := range contents {
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(content["parts"], &parts); err != nil {
			return nil, fmt.Errorf("decode native tool history parts: %w", err)
		}
		for _, part := range parts {
			for _, key := range []string{"functionCall", "functionResponse"} {
				raw, exists := part[key]
				if !exists {
					continue
				}
				alias, ok := eligible[gjson.GetBytes(raw, "id").String()]
				if !ok || gjson.GetBytes(raw, "name").String() != alias.clientName {
					continue
				}
				var function map[string]json.RawMessage
				if err := json.Unmarshal(raw, &function); err != nil {
					return nil, fmt.Errorf("decode native tool history function: %w", err)
				}
				function["id"] = json.RawMessage(strconv.Quote(alias.nativeID))
				function["name"] = json.RawMessage(strconv.Quote(alias.nativeName))
				if key == "functionCall" {
					function["args"] = alias.nativeArgs
				}
				encoded, err := json.Marshal(function)
				if err != nil {
					return nil, fmt.Errorf("encode native tool history function: %w", err)
				}
				part[key] = encoded
			}
		}
		encoded, err := json.Marshal(parts)
		if err != nil {
			return nil, fmt.Errorf("encode native tool history parts: %w", err)
		}
		content["parts"] = encoded
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		return nil, fmt.Errorf("encode native tool history contents: %w", err)
	}
	request["contents"] = encoded
	encoded, err = json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode native tool history request: %w", err)
	}
	doc["request"] = encoded
	return json.Marshal(doc)
}
