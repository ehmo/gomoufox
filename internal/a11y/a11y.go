package a11y

import (
	"errors"
	"fmt"
)

var (
	ErrStaleRef     = errors.New("stale_ref")
	ErrAmbiguousRef = errors.New("ambiguous_ref")
	ErrUnknownRef   = errors.New("unknown_ref")
)

type Element struct {
	Ref        string `json:"ref,omitempty"`
	Role       string `json:"role"`
	Name       string `json:"name"`
	Level      int    `json:"level,omitempty"`
	Value      string `json:"value,omitempty"`
	ValueKind  string `json:"value_kind,omitempty"`
	Href       string `json:"href,omitempty"`
	Required   bool   `json:"required,omitempty"`
	Resolver   string `json:"-"`
	Occurrence int    `json:"-"`
}

type Snapshot struct {
	URL     string
	Counter int
	Items   []Element
}

type Store struct {
	url     string
	counter int
	refs    map[string]Element
}

func NewStore() *Store {
	return &Store{refs: map[string]Element{}}
}

func (s *Store) Capture(url string, nodes []Element) Snapshot {
	s.url = url
	s.counter++
	s.refs = map[string]Element{}
	seen := map[string]int{}
	items := make([]Element, 0, len(nodes))
	for i, node := range nodes {
		key := node.Role + "\x00" + node.Name
		seen[key]++
		node.Occurrence = seen[key]
		if node.Resolver == "" {
			node.Resolver = fmt.Sprintf("index:%d", i)
		}
		node.Ref = fmt.Sprintf("e%d", i+1)
		stored := node
		stored.Value = ""
		stored.ValueKind = ""
		s.refs[node.Ref] = stored
		items = append(items, node)
	}
	return Snapshot{URL: url, Counter: s.counter, Items: items}
}

func (s *Store) Clear() {
	s.counter++
	s.refs = map[string]Element{}
}

func (s *Store) Resolve(ref string, current []Element) (Element, error) {
	want, ok := s.refs[ref]
	if !ok {
		return Element{}, ErrUnknownRef
	}
	candidates := []Element{}
	seen := map[string]int{}
	for i, node := range current {
		key := node.Role + "\x00" + node.Name
		seen[key]++
		node.Occurrence = seen[key]
		if node.Resolver == "" {
			node.Resolver = fmt.Sprintf("index:%d", i)
		}
		if node.Resolver == want.Resolver && node.Role == want.Role && node.Name == want.Name {
			node.Ref = ref
			candidates = append(candidates, node)
		}
	}
	if len(candidates) == 0 {
		return Element{}, ErrStaleRef
	}
	if want.Occurrence == 0 && len(candidates) > 1 {
		return Element{}, ErrAmbiguousRef
	}
	for _, candidate := range candidates {
		if want.Occurrence == 0 || candidate.Occurrence == want.Occurrence {
			return candidate, nil
		}
	}
	return Element{}, ErrStaleRef
}
