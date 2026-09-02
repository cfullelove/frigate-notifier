package filter

import "github.com/example/frigate-notifier/internal/config"

type Decision struct {
	Allow bool
	Rule  string
}

func Evaluate(c config.Filter, alarm, profile string) Decision {
	for _, r := range c.Rules {
		if matches(r.AlarmStates, alarm) && matches(r.ProfileNames, profile) {
			return Decision{r.Action == "allow", r.Name}
		}
	}
	return Decision{c.DefaultAction == "allow", "default"}
}
func matches(values []string, v string) bool {
	if len(values) == 0 {
		return true
	}
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
