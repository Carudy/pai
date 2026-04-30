package agent

import (
	"encoding/json"
	"fmt"
)

type ActionType string

const (
	ActionExecute   ActionType = "execute"
	ActionAsk       ActionType = "ask"
	ActionInfo      ActionType = "info"
	ActionDone      ActionType = "done"
	ActionTerminate ActionType = "terminate"
)

type AgentResponse struct {
	Action  ActionType      `json:"action"`
	Payload json.RawMessage `json:"payload"`
	Reason  string          `json:"reason"`
}

func ParseAgentResponse(content string) (*AgentResponse, error) {
	json_str, err := ExtractJSON(content)
	if err != nil {
		return nil, err
	}

	var resp AgentResponse
	if err := json.Unmarshal([]byte(json_str), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse agent JSON: %w\nraw: %s", err, json_str)
	}
	return &resp, nil
}

func (r *AgentResponse) GetCommand() (string, error) {
	if r.Action != ActionExecute {
		return "", fmt.Errorf("not an execute action")
	}
	var cmd string
	if err := json.Unmarshal(r.Payload, &cmd); err == nil {
		return cmd, nil
	}
	return string(r.Payload), nil
}

func (r *AgentResponse) GetQuestion() (string, error) {
	if r.Action != ActionAsk {
		return "", fmt.Errorf("not an ask action")
	}
	var q string
	if err := json.Unmarshal(r.Payload, &q); err == nil {
		return q, nil
	}
	return string(r.Payload), nil
}

func (r *AgentResponse) GetInfo() (string, error) {
	if r.Action != ActionInfo {
		return "", fmt.Errorf("not an info action")
	}
	var info string
	if err := json.Unmarshal(r.Payload, &info); err == nil {
		return info, nil
	}
	return string(r.Payload), nil
}
