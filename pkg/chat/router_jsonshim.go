package chat

import "encoding/json"

func defaultJSONMarshal(v any) ([]byte, error) { return json.Marshal(v) }
