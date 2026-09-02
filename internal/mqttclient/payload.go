package mqttclient

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type ReviewMessage struct {
	Type   string      `json:"type"`
	Before *ReviewData `json:"before"`
	After  *ReviewData `json:"after"`
}
type ReviewData struct {
	ID                      string        `json:"id"`
	ProfileName             string        `json:"profile_name"`
	State                   string        `json:"state"`
	StartTime               json.Number   `json:"start_time"`
	EndTime                 json.Number   `json:"end_time"`
	LinkedEvents            []LinkedEvent `json:"linked_events"`
	Objects, Cameras, Zones []string
}
type LinkedEvent struct {
	ID     string `json:"id"`
	Camera string `json:"camera"`
}

func Parse(b []byte) (ReviewMessage, error) {
	var raw struct {
		Message json.RawMessage `json:"message"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m ReviewMessage
	if e := d.Decode(&m); e == nil && m.Type != "" {
		return m, nil
	}
	if e := json.Unmarshal(b, &raw); e != nil || len(raw.Message) == 0 {
		return ReviewMessage{}, fmt.Errorf("invalid review payload")
	}
	d = json.NewDecoder(bytes.NewReader(raw.Message))
	d.UseNumber()
	if e := d.Decode(&m); e != nil {
		return m, e
	}
	return m, nil
}
func (m ReviewMessage) Validate() error {
	if m.Type != "new" && m.Type != "end" {
		return fmt.Errorf("unsupported type %q", m.Type)
	}
	if m.After == nil || m.After.ID == "" {
		return fmt.Errorf("missing after review ID")
	}
	if m.Type == "new" && (len(m.After.LinkedEvents) == 0 || m.After.LinkedEvents[0].ID == "") {
		return fmt.Errorf("missing linked event")
	}
	if m.Type == "end" && (m.After.StartTime == "" || m.After.EndTime == "") {
		return fmt.Errorf("missing clip timestamps")
	}
	return nil
}
