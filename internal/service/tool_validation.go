package service

import "errors"

// Only validation issues carry model-visible field details. SQL, transport,
// authorization and other internal errors never pass through this projection.
type ToolValidationIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *ToolValidationIssue) Error() string  { return e.Path + ": " + e.Message }
func (e *ToolValidationIssue) Unwrap() error  { return ErrToolInput }
func invalidField(path, message string) error { return &ToolValidationIssue{path, message} }

func ValidationIssues(err error) []ToolValidationIssue {
	var issue *ToolValidationIssue
	if errors.As(err, &issue) {
		return []ToolValidationIssue{*issue}
	}
	return []ToolValidationIssue{{Path: "strategy", Message: "Invalid strategy structure; follow the tool's strategy schema."}}
}
