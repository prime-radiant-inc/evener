package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// setThreadModelWithResume applies a model pick. A live session takes the set
// straight to its daemon. A cold session folds the pick into the resume the hub
// launches for it, so the session starts on the picked model: no second set
// runs against the fresh service (#6696), and a malformed or unlisted pick
// refuses the whole resume before any child starts. The pick threads the
// hub-internal resume only — the wire keeps thread/model/set's shape, so an
// older hub still receives the set and applies it after its own resume.
func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, relays *hubRelay, params appwire.ThreadModelSetParams) error {
	_, err := withSessionActionOwnership(ctx, cfg, params.Ref, "", func() (struct{}, error) {
		return threadActionAttempt(ctx, sources, params.Ref, "model", func(source appsource.Source) (struct{}, error) {
			return struct{}{}, source.SetThreadModel(ctx, params)
		})
	})
	if err == nil || !isSessionUnavailableError(err) {
		return err
	}
	// The fold mirrors withSessionResume's contract: only a ref the hub knows
	// is resurrected, and the pick must name an unambiguous model before any
	// child starts.
	if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
		return err
	}
	modelOverride, refusal := pickedModelRef(params)
	if refusal != "" {
		return appwire.InvalidParams(refusal)
	}
	resumed, resumeErr := hubThreadAutoResumeWithModel(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref}, modelOverride)
	if resumeErr != nil {
		return resumeErr
	}
	// The resumed daemon advertises the picked model itself; the relay hands
	// its frames to subscribers the way the direct set's changed frame did.
	if relays != nil {
		if err := relays.startRelayForThread(ctx, resumed.Thread); err != nil {
			return err
		}
	}
	return nil
}

// pickedModelRef renders thread/model/set's pick as the qualified model ref the
// folded resume starts on. The fold runs only where the pick has no live
// session to resolve against, so it names an instance and a model together: a
// pick naming neither or only one names no unambiguous model identity, and it
// never starts a session onto a guess.
func pickedModelRef(params appwire.ThreadModelSetParams) (modelRef string, refusal string) {
	switch {
	case params.Model == "" && params.ModelProvider == "":
		return "", "model is required"
	case params.Model == "" || params.ModelProvider == "":
		return "", "model and modelProvider are named together: a picked model names its provider instance"
	}
	return params.ModelProvider + "/" + params.Model, ""
}
