// Package interaction defines the context carried from a chat platform to a
// command executor. It is intentionally independent from Feishu so other
// frontends can reuse the same execution and audit pipeline later.
package interaction

// Request is one user instruction together with the identity and delivery
// metadata required for authorization, deduplication, and audit records.
type Request struct {
	Text           string
	OperatorOpenID string
	OperatorName   string
	ChatID         string
	MessageID      string
}
