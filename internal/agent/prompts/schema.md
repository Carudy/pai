# PAI Agent Schema

This document defines the standard JSON response format for all PAI agents.

## Response Schema

All agent responses must conform to this JSON schema:

```json
{
  "action": {
    "type": "string",
    "enum": ["execute", "ask", "info", "done", "terminate"]
  },
  "payload": {
    "type": ["string", "object", "array"],
    "description": "The main content based on action type"
  },
  "reason": {
    "type": "string",
    "description": "Explanation for the action"
  }
}
```

## Action Types

- `execute`: Run a command. Payload contains the command string.
- `ask`: Request information from the user. Payload contains the question.
- `info`: Provide information to the user. Payload contains the message.
- `done`: Task completed successfully. Payload contains the summary.
- `terminate`: Cannot complete the task. Payload contains the reason.

## Payload Format by Action Type

### execute
```json
{
  "action": "execute",
  "payload": "command string to run",
  "reason": "why this command is needed"
}
```

### ask
```json
{
  "action": "ask",
  "payload": "question for the user",
  "reason": "why this information is needed"
}
```

### info
```json
{
  "action": "info",
  "payload": "information for the user",
  "reason": "why this information is relevant"
}
```

### done
```json
{
  "action": "done",
  "payload": "summary of completion",
  "reason": "assessment of the outcome"
}
```

### terminate
```json
{
  "action": "terminate",
  "payload": "reason for termination",
  "reason": "why the task cannot be completed"
}
```
