package identifier

func newDomainID(prefix string) (string, error) {
	payload, err := newUUIDv7Payload()
	if err != nil {
		return "", err
	}
	return prefix + payload, nil
}

func validateDomainID(value, prefix string) error {
	if len(value) != len(prefix)+base62Width || len(value) < len(prefix) || value[:len(prefix)] != prefix {
		return errInvalidUUIDPayload
	}
	return ValidateUUIDv7Payload(value[len(prefix):])
}

func NewSessionID() (string, error)          { return newDomainID("") }
func NewInstallationID() (string, error)     { return newDomainID("") }
func NewDelegateID() (string, error)         { return newDomainID("dlg_") }
func NewWatchID() (string, error)            { return newDomainID("watch_") }
func NewWatchGeneration() (string, error)    { return newDomainID("wg_") }
func NewWatchDeliveryID() (string, error)    { return newDomainID("wd_") }
func NewAgentCallID() (string, error)        { return newDomainID("ag_") }
func NewAPIAttemptID() (string, error)       { return newDomainID("att_") }
func NewSyntheticCallID() (string, error)    { return newDomainID("call_") }
func NewClientMutationID() (string, error)   { return newDomainID("") }
func NewTerminalGeneration() (string, error) { return newDomainID("") }
func NewTurnID() (string, error)             { return newDomainID("t_") }
func NewRoundID() (string, error)            { return newDomainID("r_") }

func ValidateSessionID(value string) error          { return validateDomainID(value, "") }
func ValidateInstallationID(value string) error     { return validateDomainID(value, "") }
func ValidateDelegateID(value string) error         { return validateDomainID(value, "dlg_") }
func ValidateWatchID(value string) error            { return validateDomainID(value, "watch_") }
func ValidateWatchGeneration(value string) error    { return validateDomainID(value, "wg_") }
func ValidateWatchDeliveryID(value string) error    { return validateDomainID(value, "wd_") }
func ValidateAgentCallID(value string) error        { return validateDomainID(value, "ag_") }
func ValidateAPIAttemptID(value string) error       { return validateDomainID(value, "att_") }
func ValidateSyntheticCallID(value string) error    { return validateDomainID(value, "call_") }
func ValidateClientMutationID(value string) error   { return validateDomainID(value, "") }
func ValidateTerminalGeneration(value string) error { return validateDomainID(value, "") }
func ValidateTurnID(value string) error             { return validateDomainID(value, "t_") }
func ValidateRoundID(value string) error            { return validateDomainID(value, "r_") }

func mustDomainID(newID func() (string, error)) string {
	value, err := newID()
	if err != nil {
		panic(err)
	}
	return value
}

func MustNewSessionID() string          { return mustDomainID(NewSessionID) }
func MustNewInstallationID() string     { return mustDomainID(NewInstallationID) }
func MustNewDelegateID() string         { return mustDomainID(NewDelegateID) }
func MustNewWatchID() string            { return mustDomainID(NewWatchID) }
func MustNewWatchGeneration() string    { return mustDomainID(NewWatchGeneration) }
func MustNewWatchDeliveryID() string    { return mustDomainID(NewWatchDeliveryID) }
func MustNewAgentCallID() string        { return mustDomainID(NewAgentCallID) }
func MustNewAPIAttemptID() string       { return mustDomainID(NewAPIAttemptID) }
func MustNewSyntheticCallID() string    { return mustDomainID(NewSyntheticCallID) }
func MustNewClientMutationID() string   { return mustDomainID(NewClientMutationID) }
func MustNewTerminalGeneration() string { return mustDomainID(NewTerminalGeneration) }
func MustNewTurnID() string             { return mustDomainID(NewTurnID) }
func MustNewRoundID() string            { return mustDomainID(NewRoundID) }
