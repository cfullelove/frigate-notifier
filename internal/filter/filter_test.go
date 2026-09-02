package filter

import (
	"github.com/example/frigate-notifier/internal/config"
	"testing"
)

func TestOrderedRules(t *testing.T) {
	c := config.Filter{DefaultAction: "deny", Rules: []config.Rule{{Name: "deny-first", Action: "deny", AlarmStates: []string{"armed"}}, {Name: "allow", Action: "allow", AlarmStates: []string{"armed"}}}}
	d := Evaluate(c, "armed", "x")
	if d.Allow || d.Rule != "deny-first" {
		t.Fatal(d)
	}
}
func TestRequiredCases(t *testing.T) {
	c := config.Filter{DefaultAction: "deny", Rules: []config.Rule{{Name: "away", Action: "allow", AlarmStates: []string{"armed_away"}}, {Name: "home", Action: "allow", AlarmStates: []string{"armed_home"}, ProfileNames: []string{"Car Port"}}}}
	for _, x := range []struct {
		s, p string
		yes  bool
	}{{"armed_away", "Anything", true}, {"armed_home", "Car Port", true}, {"armed_home", "Outside", false}, {"disarmed", "Car Port", false}} {
		if Evaluate(c, x.s, x.p).Allow != x.yes {
			t.Fatalf("%+v", x)
		}
	}
}
