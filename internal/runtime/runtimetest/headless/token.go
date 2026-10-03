package headless

// TokenProbe is a workload's native view of its own token and the profile
// paths it resolves from that token. Paths and SIDs are private evidence.
type TokenProbe struct {
	// PID and Created identify the probing process, so its token can be
	// matched with the process the observer held.
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	SID     string `json:"sid"`
	// Source is the token source name, such as the product's S4U source.
	Source           string           `json:"source"`
	AuthenticationID string           `json:"authenticationId"`
	Session          uint32           `json:"session"`
	Integrity        string           `json:"integrity"`
	Elevated         bool             `json:"elevated"`
	ElevationType    uint32           `json:"elevationType"`
	Groups           []TokenGroup     `json:"groups"`
	Privileges       []TokenPrivilege `json:"privileges"`
	// LogonType and AuthPackage come from LsaGetLogonSessionData; a refused
	// query is recorded in LogonSessionError instead.
	LogonType         uint32            `json:"logonType,omitempty"`
	AuthPackage       string            `json:"authPackage,omitempty"`
	LogonSessionError uint32            `json:"logonSessionError,omitempty"`
	KnownFolders      map[string]string `json:"knownFolders,omitempty"`
	KnownFolderErrors map[string]uint32 `json:"knownFolderErrors,omitempty"`
}

// TokenGroup is one group SID and its attributes.
type TokenGroup struct {
	SID        string `json:"sid"`
	Attributes uint32 `json:"attributes"`
}

// TokenPrivilege is one privilege and its attributes.
type TokenPrivilege struct {
	Name       string `json:"name"`
	Attributes uint32 `json:"attributes"`
}
