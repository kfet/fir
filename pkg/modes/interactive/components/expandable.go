package components

// Expandable is implemented by components that support expand/collapse toggling.
type Expandable interface {
	SetExpanded(expanded bool)
}
