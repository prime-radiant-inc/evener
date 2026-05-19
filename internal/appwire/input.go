package appwire

func (p TurnStartParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p ThreadStartParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p TurnStartParams) TargetRef() string {
	return p.ThreadID
}

func (p TurnSteerParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p TurnSteerParams) EffectiveTurnID() string {
	return p.ExpectedTurnID
}

func (p TurnSteerParams) TargetRef() string {
	return p.ThreadID
}

func (p TurnInterruptParams) EffectiveTurnID() string {
	return p.ExpectedTurnID
}

func (p TurnInterruptParams) TargetRef() string {
	return p.ThreadID
}
