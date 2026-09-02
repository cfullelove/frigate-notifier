package gemini

import "testing"

func TestParse(t *testing.T) {
	r, e := Parse(`{"has_person":true,"description":"visitor"}`)
	if e != nil || !r.HasPerson {
		t.Fatal(r, e)
	}
	for _, s := range []string{`{"has_person":true}`, `{"has_person":"true","description":"x"}`, `{"has_person":null,"description":"x"}`, `{"has_person":true,"description":"x","extra":1}`, "```json\n{}\n```"} {
		if _, e := Parse(s); e == nil {
			t.Fatal(s)
		}
	}
}
