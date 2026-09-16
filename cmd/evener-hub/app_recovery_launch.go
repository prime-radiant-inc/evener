package hub

import (
	"slices"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// recoverInterruptedLaunches settles durable launch intents left by a hub that
// died between a launch's durable exit-proof invalidation (BeforeLaunch, or
// BeginLaunchGuard for the retirement path) and the child's rendezvous claim.
//
// A group is left exactly as it was while any alias is still claimed — the same
// discovery authority force stop uses, where even a stale or foreign marker
// must take the ordinary verified-process path — so a previous process's exit
// proof can never describe a new child. An unclaimed group has no child under
// that authority, so its launch intent is cleared and its prior exit proof
// restored; only then can the ordinary resume and force stop paths run instead
// of rejecting the session forever.
func recoverInterruptedLaunches(cfg hubcore.WebConfig) error {
	if cfg.ResumeLocks == nil || cfg.RunDir == "" {
		return nil
	}
	return cfg.ResumeLocks.RecoverInterruptedLaunches(func(aliases []string) (bool, error) {
		entries, err := rendezvous.ListStrict(cfg.RunDir)
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			for _, alias := range forceStopAliases(entry) {
				if slices.Contains(aliases, alias) {
					return true, nil
				}
			}
		}
		return false, nil
	})
}
