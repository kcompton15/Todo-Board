package store

import "encoding/json"

// Request decoders reject null, so omission must be absent on the wire.
func (op Operation) MarshalJSON() ([]byte, error) {
	type plain Operation
	data, err := json.Marshal(plain(op))
	if err != nil || op.Subtasks != nil {
		return data, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	delete(fields, "subtasks")
	return json.Marshal(fields)
}
