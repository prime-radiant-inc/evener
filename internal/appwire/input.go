package appwire

import "strings"

func (p TurnStartParams) EffectiveInput() []InputItem {
	if len(p.Input) > 0 {
		return p.Input
	}
	return p.Items
}

func (p ThreadStartParams) EffectiveInput() []InputItem {
	if len(p.Input) > 0 {
		return p.Input
	}
	return p.Items
}

func (p TurnStartParams) TargetRef() string {
	if strings.TrimSpace(p.Ref) != "" {
		return p.Ref
	}
	return p.ThreadID
}

func (p TurnSteerParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p TurnSteerParams) EffectiveTurnID() string {
	if strings.TrimSpace(p.ExpectedTurnID) != "" {
		return p.ExpectedTurnID
	}
	return p.TurnID
}

func (p TurnSteerParams) TargetRef() string {
	if strings.TrimSpace(p.Ref) != "" {
		return p.Ref
	}
	return p.ThreadID
}

func (p TurnInterruptParams) EffectiveTurnID() string {
	if strings.TrimSpace(p.ExpectedTurnID) != "" {
		return p.ExpectedTurnID
	}
	return p.TurnID
}

func (p TurnInterruptParams) TargetRef() string {
	if strings.TrimSpace(p.Ref) != "" {
		return p.Ref
	}
	return p.ThreadID
}
