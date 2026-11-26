package core

// ModelMentionOption describes a model reference and its optional effort choices.
type ModelMentionOption struct {
	ID, Name, DefaultEffort string
	Alias                   bool
	Efforts                 []string
}
