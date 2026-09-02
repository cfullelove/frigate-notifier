package mqttclient

import "testing"

func TestParsePreservesFraction(t *testing.T) {
	m, e := Parse([]byte(`{"type":"new","after":{"id":"r","start_time":1788251611.071814,"linked_events":[{"id":"e"}],"unknown":1}}`))
	if e != nil || m.After.StartTime.String() != "1788251611.071814" || m.Validate() != nil {
		t.Fatal(m, e)
	}
	if _, e = Parse([]byte("bad")); e == nil {
		t.Fatal("accepted malformed")
	}
}
func TestWrappedEnd(t *testing.T) {
	m, e := Parse([]byte(`{"message":{"type":"end","after":{"id":"r","start_time":1.2,"end_time":2.3}}}`))
	if e != nil || m.Validate() != nil {
		t.Fatal(e)
	}
}
