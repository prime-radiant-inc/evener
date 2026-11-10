package repoaudit

import "primeradiant.com/evener/internal/knobtable"

// everyKnobHasAConsumer is the audit the knob table exists for: a registered
// variable or hub.toml key that no code reads is dead and should be deleted,
// and a removed hub.toml key belongs in knobtable's removedKnobs.
func everyKnobHasAConsumer(a *audit) {
	envVars, hubKeys, err := knobtable.UnconsumedKnobs(".")
	if err != nil {
		a.Fatal(err)
	}
	for _, name := range envVars {
		a.Errorf("%s is registered in envvars but nothing reads it", name)
	}
	for _, key := range hubKeys {
		a.Errorf("hub.toml key %s is decoded but nothing reads it", key)
	}
}
